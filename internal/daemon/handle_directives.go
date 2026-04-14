package daemon

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikehu/cmdr/internal/prompts"
	"github.com/mikehu/cmdr/internal/tmux"
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
		result, err := db.Exec(
			`INSERT INTO claude_tasks (type, status, repo_path, prompt, created_at, started_at)
			 VALUES ('directive', 'draft', ?, ?, ?, ?)`,
			body.RepoPath, body.Content, now, now,
		)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		id, _ := result.LastInsertId()

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": int(id), "status": "draft", "snippet": directiveTitle(body.Content),
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
		var oldRepo, oldIntent string
		db.QueryRow(`SELECT COALESCE(repo_path, ''), COALESCE(intent, '') FROM claude_tasks WHERE id=?`, body.ID).
			Scan(&oldRepo, &oldIntent)

		db.Exec(`UPDATE claude_tasks SET repo_path=?, prompt=?, intent=? WHERE id=? AND status='draft'`,
			body.RepoPath, body.Content, body.Intent, body.ID)

		snippet := directiveTitle(body.Content)
		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": body.ID, "status": "draft", "repoPath": body.RepoPath, "intent": body.Intent, "snippet": snippet,
		}})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// handleSubmitDirective launches Claude with the draft's prompt in a tmux window.
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

		if checkUnpushed(w, repoPath) {
			return
		}

		res, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:         body.ID,
			Intent:         body.Intent,
			UserPrompt:     prompt,
			RepoPath:       repoPath,
			Session:        "", // auto-detect from repo
			WindowPrefix:   "task",
			WorktreePrefix: "directive",
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

// findOrCreateSession finds an existing tmux session for the repo or creates one.
func findOrCreateSession(repoPath string) (string, error) {
	sessions, _ := tmux.ListSessions()
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

	return tmux.CreateSession(repoPath)
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
