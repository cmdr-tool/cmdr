package daemon

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/prompts"
)

// handleCreateDirective creates a new claude_task in draft status.
func handleCreateDirective(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			RepoPath string `json:"repoPath"`
			Content  string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		now := time.Now().Format(time.RFC3339)
		title := directiveTitle(body.Content)
		result, err := db.Exec(
			`INSERT INTO claude_tasks (type, status, repo_path, prompt, title, created_at, started_at)
			 VALUES ('directive', 'draft', ?, ?, ?, ?, ?)`,
			body.RepoPath, body.Content, title, now, now,
		)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		id, _ := result.LastInsertId()

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": int(id), "status": "draft",
		}})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": int(id), "status": "draft"})
	}
}

// handleSaveDirective updates the prompt content of a draft task.
func handleSaveDirective(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID       int    `json:"id"`
			RepoPath string `json:"repoPath"`
			Content  string `json:"content"`
			Intent   string `json:"intent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"id is required"}`, http.StatusBadRequest)
			return
		}

		// Read old values to diff against
		var oldRepo, oldIntent, oldTitle string
		db.QueryRow(`SELECT COALESCE(repo_path, ''), COALESCE(intent, ''), COALESCE(title, '') FROM claude_tasks WHERE id=?`, body.ID).
			Scan(&oldRepo, &oldIntent, &oldTitle)

		title := directiveTitle(body.Content)
		db.Exec(`UPDATE claude_tasks SET repo_path=?, prompt=?, intent=?, title=? WHERE id=? AND status='draft'`,
			body.RepoPath, body.Content, body.Intent, title, body.ID)

		if body.RepoPath != oldRepo || body.Intent != oldIntent || title != oldTitle {
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": body.ID, "status": "draft", "repoPath": body.RepoPath, "intent": body.Intent, "title": title,
			}})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// handleSubmitDirective launches Claude with the draft's prompt.
// Headless intents (e.g. analysis) run via claude -p with streaming.
// Interactive intents launch in a tmux window.
func handleSubmitDirective(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID     int    `json:"id"`
			Intent string `json:"intent"` // optional intent ID (e.g. "bug-fix")
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"id is required"}`, http.StatusBadRequest)
			return
		}

		var repoPath, prompt string
		err := db.QueryRow(`SELECT repo_path, prompt FROM claude_tasks WHERE id=? AND status='draft'`, body.ID).
			Scan(&repoPath, &prompt)
		if err != nil {
			http.Error(w, `{"error":"draft not found"}`, http.StatusNotFound)
			return
		}

		if repoPath == "" || prompt == "" {
			http.Error(w, `{"error":"draft must have a repo and content"}`, http.StatusBadRequest)
			return
		}

		// Headless intents: run via claude -p (no tmux window)
		if prompts.IntentIsHeadless(body.Intent) {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='running', intent=?, started_at=? WHERE id=?`,
				body.Intent, now, body.ID)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": body.ID, "status": "running", "intent": body.Intent, "repoPath": repoPath,
			}})

			systemPrompt, _ := prompts.GetIntentPrompt(body.Intent)

			go runHeadless(db, bus, HeadlessConfig{
				TaskID:       body.ID,
				Prompt:       prompt,
				WorkDir:      repoPath,
				SystemPrompt: systemPrompt,
			})

			enhanceTitle(db, bus, body.ID, truncate(prompt, 500))

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		if checkUnpushed(w, repoPath) {
			return
		}

		// Derive window prefix from intent; fall back to "task" for no-intent directives
		windowPrefix := "task"
		if body.Intent != "" {
			windowPrefix = body.Intent
		}

		res, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:         body.ID,
			Intent:         body.Intent,
			UserPrompt:     prompt,
			RepoPath:       repoPath,
			WindowPrefix:   windowPrefix,
		})
		if err != nil {
			log.Printf("cmdr: directive/submit: %v", err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		enhanceTitle(db, bus, body.ID, truncate(prompt, 500))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"target":  res.Target,
			"session": res.Session,
		})
	}
}

// findOrCreateSession finds an existing terminal session for the repo or creates one.
func findOrCreateSession(repoPath string) (string, error) {
	sessions, _ := term.ListSessions()
	resolved := repoPath
	if r, err := resolveSymlinks(repoPath); err == nil {
		resolved = r
	}

	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.CWD == resolved || p.CWD == repoPath {
					return s.Name, nil
				}
			}
		}
	}

	return term.CreateSession(repoPath)
}

// handleListIntents returns available directive intent presets.
func handleListIntents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(prompts.ListIntents())
	}
}

func resolveSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// directiveTitle extracts a title from directive markdown content.
// Takes the first non-empty line, truncated to 80 chars.
// Code refs (@file) are used as-is. Image blocks are skipped.
func directiveTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "![") {
			continue
		}
		// Strip markdown heading prefix
		line = strings.TrimLeft(line, "# ")
		if len(line) > 80 {
			return line[:77] + "..."
		}
		return line
	}
	return "Untitled directive"
}
