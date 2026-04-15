package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mikehu/cmdr/internal/ollama"
	"github.com/mikehu/cmdr/internal/prompts"
	"github.com/mikehu/cmdr/internal/tmux"
)

// --- Task CRUD ---

func handleListClaudeTasks(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := `SELECT id, type, status, repo_path, commit_sha, COALESCE(title, ''), COALESCE(pr_url, ''), error_msg, created_at, started_at, completed_at, COALESCE(prompt, ''), COALESCE(intent, ''), parent_id
			FROM claude_tasks ORDER BY created_at DESC LIMIT 50`
		rows, err := db.Query(query)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type task struct {
			ID          int     `json:"id"`
			Type        string  `json:"type"`
			Status      string  `json:"status"`
			RepoPath    string  `json:"repoPath"`
			CommitSHA   string  `json:"commitSha"`
			Title       string  `json:"title,omitempty"`
			PRUrl       string  `json:"prUrl,omitempty"`
			ErrorMsg    string  `json:"errorMsg,omitempty"`
			CreatedAt   string  `json:"createdAt"`
			StartedAt   *string `json:"startedAt"`
			CompletedAt *string `json:"completedAt"`
			Intent      string  `json:"intent,omitempty"`
			ParentID    *int    `json:"parentId,omitempty"`
			prompt      string
		}

		var taskList []task
		for rows.Next() {
			var t task
			if err := rows.Scan(&t.ID, &t.Type, &t.Status, &t.RepoPath, &t.CommitSHA, &t.Title, &t.PRUrl,
				&t.ErrorMsg, &t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.prompt, &t.Intent, &t.ParentID); err != nil {
				continue
			}
			taskList = append(taskList, t)
		}
		if taskList == nil {
			taskList = []task{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(taskList)
	}
}

func handleGetClaudeTaskResult(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		var result, prompt, status, errMsg, intent string
		err := db.QueryRow(`SELECT result, prompt, status, error_msg, COALESCE(intent, '') FROM claude_tasks WHERE id = ?`, id).
			Scan(&result, &prompt, &status, &errMsg, &intent)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		// For draft tasks, return the prompt as the result
		content := result
		if status == "draft" {
			content = prompt
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"result":   content,
			"status":   status,
			"errorMsg": errMsg,
			"intent":   intent,
		})
	}
}

func handleUpdateClaudeTaskResult(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID     int    `json:"id"`
			Result string `json:"result"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"missing id or result"}`, http.StatusBadRequest)
			return
		}

		title := extractTitle(body.Result)
		db.Exec(`UPDATE claude_tasks SET result=?, title=? WHERE id=?`, body.Result, title, body.ID)

		enhanceTitle(db, bus, body.ID, truncate(body.Result, 1000))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleDismissClaudeTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID    int    `json:"id"`
			All   string `json:"all"`   // "completed" to clear all completed
			Force bool   `json:"force"` // required to dismiss running tasks
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		// Guard: don't dismiss running tasks without explicit confirmation
		if body.ID > 0 {
			var status string
			if err := db.QueryRow(`SELECT status FROM claude_tasks WHERE id = ?`, body.ID).Scan(&status); err == nil {
				if status == "running" && !body.Force {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(map[string]any{
						"error":         "task is still running",
						"requiresForce": true,
						"status":        status,
					})
					return
				}
			}
		}

		// Clean up worktrees and kill tmux windows for tasks being dismissed
		if body.ID > 0 {
			killTaskWindow(db, body.ID)
			cleanupTaskWorktree(db, body.ID)
		} else if body.All == "completed" {
			cleanupAllTaskWorktrees(db)
		}

		var res sql.Result
		var err error
		if body.All == "completed" {
			res, err = db.Exec(`DELETE FROM claude_tasks WHERE status IN ('failed', 'completed') AND type != 'delegation'`)
		} else if body.ID > 0 {
			res, err = db.Exec(`DELETE FROM claude_tasks WHERE id = ?`, body.ID)
		} else {
			http.Error(w, `{"error":"missing id or all"}`, http.StatusBadRequest)
			return
		}

		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()

		if body.ID > 0 && n > 0 {
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": body.ID, "status": "dismissed",
			}})
		} else if body.All == "completed" && n > 0 {
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": 0, "status": "dismissed",
			}})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"dismissed": n})
	}
}

// killTaskWindow kills the tmux window for a task if it's still alive.
func killTaskWindow(db *sql.DB, taskID int) {
	var taskType, intent string
	if err := db.QueryRow(`SELECT type, COALESCE(intent, '') FROM claude_tasks WHERE id = ?`, taskID).Scan(&taskType, &intent); err != nil {
		return
	}
	windowName := taskWindowName(taskType, intent, taskID)

	// Find the window across all sessions and kill it
	sessions, err := tmux.ListSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		for _, w := range s.Windows {
			if w.Name == windowName {
				target := fmt.Sprintf("%s:%s", s.Name, w.Name)
				exec.Command("tmux", "kill-window", "-t", target).Run()
				log.Printf("cmdr: killed tmux window %s (task %d)", target, taskID)
				return
			}
		}
	}
}

// handleCancelTask stops a running task. For interactive directives, restores
// to draft. For headless tasks (ask, analysis directives), kills the process.
func handleCancelTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID int `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"id is required"}`, http.StatusBadRequest)
			return
		}

		var taskType, status, intent string
		err := db.QueryRow(`SELECT type, status, COALESCE(intent, '') FROM claude_tasks WHERE id = ?`, body.ID).Scan(&taskType, &status, &intent)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		if status != "running" {
			http.Error(w, `{"error":"task is not running"}`, http.StatusConflict)
			return
		}

		// Headless tasks (ask, headless directives): kill process, mark cancelled
		if taskType == "ask" || prompts.IntentIsHeadless(intent) {
			cancelHeadlessProcess(body.ID)
			now := time.Now().Format(time.RFC3339)
			if taskType == "directive" {
				// Headless directives reset to draft like interactive ones
				db.Exec(`UPDATE claude_tasks SET status='draft', intent='', worktree='', started_at=NULL, completed_at=NULL, result='', error_msg='', pr_url='' WHERE id=?`, body.ID)
				bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
					"id": body.ID, "type": "done",
				}})
				bus.Publish(Event{Type: "claude:task", Data: map[string]any{
					"id": body.ID, "status": "draft",
				}})
				log.Printf("cmdr: headless directive %d cancelled, restored to draft", body.ID)
			} else {
				db.Exec(`UPDATE claude_tasks SET status='failed', error_msg='cancelled', completed_at=? WHERE id=?`, now, body.ID)
				bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
					"id": body.ID, "type": "done",
				}})
				bus.Publish(Event{Type: "claude:task", Data: map[string]any{
					"id": body.ID, "status": "failed",
				}})
				log.Printf("cmdr: ask %d cancelled", body.ID)
			}
		} else if taskType == "directive" {
			// Interactive directives: kill tmux window, reset to draft
			killTaskWindow(db, body.ID)
			cleanupTaskWorktree(db, body.ID)
			db.Exec(`UPDATE claude_tasks SET status='draft', intent='', worktree='', started_at=NULL, completed_at=NULL, result='', error_msg='', pr_url='' WHERE id=?`, body.ID)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": body.ID, "status": "draft",
			}})
			log.Printf("cmdr: directive %d cancelled, restored to draft", body.ID)
		} else {
			http.Error(w, `{"error":"cancel not supported for this task type"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleResolveTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID    int    `json:"id"`
			PRUrl string `json:"prUrl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		now := time.Now().Format(time.RFC3339)
		db.Exec(`UPDATE claude_tasks SET status='completed', pr_url=?, completed_at=? WHERE id=?`,
			body.PRUrl, now, body.ID)

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": body.ID, "status": "completed", "prUrl": body.PRUrl,
		}})

		log.Printf("cmdr: task %d completed (PR: %s)", body.ID, body.PRUrl)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "completed", "prUrl": body.PRUrl})
	}
}

// --- Task launch (config-driven) ---

// TaskLaunchConfig describes how to launch a Claude session in tmux.
type TaskLaunchConfig struct {
	TaskID         int
	Intent         string // optional intent ID for --append-system-prompt
	UserPrompt     string // the content to send to Claude
	RepoPath       string
	WindowPrefix   string // e.g. "refactor", "task" → "refactor-42", "task-42"
	WorktreePrefix string // overrides WindowPrefix for worktree naming; defaults to WindowPrefix if empty
	MarkerDir      string // optional dir for writing task ID marker file (e.g. ~/.cmdr/refactors)
}

// TaskLaunchResult is returned from launchTask with session/window info.
type TaskLaunchResult struct {
	Target  string // tmux target "session:window"
	Session string
	Window  string
}

// launchTask launches a Claude session in tmux based on the given config.
func launchTask(db *sql.DB, bus *EventBus, cfg TaskLaunchConfig) (TaskLaunchResult, error) {
	windowName := fmt.Sprintf("%s-%d", cfg.WindowPrefix, cfg.TaskID)

	// Worktree prefix defaults to window prefix when not explicitly set
	worktreePrefix := cfg.WorktreePrefix
	if worktreePrefix == "" {
		worktreePrefix = cfg.WindowPrefix
	}

	// Write optional marker file
	var worktreeName string
	if worktreePrefix != "" {
		worktreeName = buildWorktreeName(worktreePrefix, cfg.TaskID)
		if cfg.MarkerDir != "" {
			os.MkdirAll(cfg.MarkerDir, 0o700)
			os.WriteFile(filepath.Join(cfg.MarkerDir, worktreeName), []byte(strconv.Itoa(cfg.TaskID)), 0o644)
		}
	}

	// Resolve image references to absolute paths Claude can read
	prompt := resolveImageRefs(cfg.UserPrompt)

	// Write prompt to a temp file to avoid tmux command length limits.
	// Tmux has a ~500 char arg limit for new-window commands; ADRs can be 20K+.
	promptDir := filepath.Join(os.TempDir(), "cmdr")
	os.MkdirAll(promptDir, 0o700)
	promptFile := filepath.Join(promptDir, fmt.Sprintf("task-%d-prompt.md", cfg.TaskID))
	os.WriteFile(promptFile, []byte(prompt), 0o644)

	// Build claude command — omit -w when no worktree prefix (e.g. delegations)
	var baseCmd string
	if cfg.WorktreePrefix != "" {
		baseCmd = fmt.Sprintf("claude -w %s --name 'cmdr-task-%d'", worktreeName, cfg.TaskID)
	} else {
		baseCmd = fmt.Sprintf("claude --name 'cmdr-task-%d'", cfg.TaskID)
	}
	var cmd string
	if cfg.Intent != "" {
		// For design-phase intents (e.g. new-feature), use the design prompt
		// for the initial dispatch; the intent prompt is used for implementation
		var systemPrompt string
		if dp, err := prompts.GetDesignPrompt(cfg.Intent); err == nil && dp != "" {
			systemPrompt = dp
		}
		if systemPrompt == "" {
			if ip, err := prompts.GetIntentPrompt(cfg.Intent); err == nil {
				systemPrompt = ip
			}
		}

		if systemPrompt != "" {
			escapedIntent := strings.ReplaceAll(systemPrompt, "'", "'\\''")
			cmd = fmt.Sprintf("exec %s --append-system-prompt '%s' < '%s'", baseCmd, escapedIntent, promptFile)
		} else {
			cmd = fmt.Sprintf("exec %s < '%s'", baseCmd, promptFile)
		}
	} else {
		// No explicit intent — apply generic guidance as baseline
		if gp, err := prompts.GetIntentPrompt("generic"); err == nil {
			escapedGeneric := strings.ReplaceAll(gp, "'", "'\\''")
			cmd = fmt.Sprintf("exec %s --append-system-prompt '%s' < '%s'", baseCmd, escapedGeneric, promptFile)
		} else {
			cmd = fmt.Sprintf("exec %s < '%s'", baseCmd, promptFile)
		}
	}

	// Resolve session and create window
	sessionName, err := findOrCreateSession(cfg.RepoPath)
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("session: %w", err)
	}
	target, err := tmux.CreateDraftWindow(sessionName, windowName, cfg.RepoPath, cmd)
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("window: %w", err)
	}

	// Update task status
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE claude_tasks SET status='running', intent=?, worktree=?, started_at=? WHERE id=?`,
		cfg.Intent, worktreeName, now, cfg.TaskID)
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": cfg.TaskID, "status": "running", "intent": cfg.Intent, "repoPath": cfg.RepoPath,
	}})

	log.Printf("cmdr: task %d launched (session %s, target %s, intent %q)", cfg.TaskID, sessionName, target, cfg.Intent)

	return TaskLaunchResult{Target: target, Session: sessionName, Window: windowName}, nil
}

// --- Launch refactor from review findings ---

func handleStartRefactor(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			TaskID int `json:"taskId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID == 0 {
			http.Error(w, `{"error":"missing taskId"}`, http.StatusBadRequest)
			return
		}

		var result, repoPath, commitSha string
		err := db.QueryRow(`SELECT result, repo_path, commit_sha FROM claude_tasks WHERE id = ?`, body.TaskID).
			Scan(&result, &repoPath, &commitSha)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		if checkUnpushed(w, repoPath) {
			return
		}

		shortSha := commitSha
		if len(shortSha) > 7 {
			shortSha = shortSha[:7]
		}

		res, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:   body.TaskID,
			Intent:   "refactor",
			RepoPath: repoPath,
			UserPrompt: fmt.Sprintf(
				"Address the following code review findings from commit %s.\n\n"+
					"## How to read these findings\n\n"+
					"- Each finding has a priority, location, issue description, and a step-by-step plan\n"+
					"- If a finding contains a `> User response:` blockquote, treat it as explicit guidance — follow it\n"+
					"- If a finding has multiple valid approaches and no user response, pick the cleanest one\n"+
					"- If a finding was removed from the review, the reviewer decided it's not applicable — skip it\n"+
					"- Only ask me if there is genuine ambiguity that requires a judgment call\n\n"+
					"## Review Findings\n\n%s",
				shortSha, result,
			),
			WindowPrefix:   "refactor",
			MarkerDir:      filepath.Join(os.Getenv("HOME"), ".cmdr", "refactors"),
		})
		if err != nil {
			log.Printf("cmdr: refactor launch failed: %v", err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": res.Target, "session": res.Session, "window": res.Window})
	}
}

// --- Launch implementation from design ADR ---

func handleStartImplementation(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			TaskID    int  `json:"taskId"`
			CommitADR bool `json:"commitADR"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID == 0 {
			http.Error(w, `{"error":"missing taskId"}`, http.StatusBadRequest)
			return
		}

		var adrContent, repoPath string
		err := db.QueryRow(`SELECT result, repo_path FROM claude_tasks WHERE id = ?`, body.TaskID).
			Scan(&adrContent, &repoPath)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		// Pre-flight: worktrees branch from HEAD, so unpushed work would be missed
		if checkUnpushed(w, repoPath) {
			return
		}

		// Build the prompt with the ADR and commit instructions
		var prompt string
		if body.CommitADR {
			prompt = fmt.Sprintf(
				"## Approved ADR\n\n"+
					"The following ADR has been approved. Commit it to `docs/` (follow the existing `ADR-NNNN-name.md` naming convention) as your first action before implementing.\n\n"+
					"```markdown\n%s\n```\n\n"+
					"## Instructions\n\nImplement the feature described in this ADR.",
				adrContent,
			)
		} else {
			prompt = fmt.Sprintf(
				"## Approved ADR\n\n"+
					"The following ADR has been approved for implementation. Do NOT commit the ADR itself to the repo — it is for context only.\n\n"+
					"%s\n\n"+
					"## Instructions\n\nImplement the feature described in this ADR.",
				adrContent,
			)
		}

		res, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:         body.TaskID,
			Intent:         "new-feature",
			RepoPath:       repoPath,
			UserPrompt:     prompt,
			WindowPrefix:   "new-feature-impl",
		})
		if err != nil {
			log.Printf("cmdr: implementation launch failed: %v", err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": res.Target, "session": res.Session, "window": res.Window})
	}
}

// --- Spawn child task from completed parent ---

// handleSpawnTask creates a new child task from a completed parent's result.
// The child inherits repo_path and gets a prompt built from the parent's context.
func handleSpawnTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ParentID  int    `json:"parentId"`
			Intent    string `json:"intent"`    // defaults to "implementation"
			CommitADR bool   `json:"commitADR"` // for ADR→implementation: commit ADR to repo
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ParentID == 0 {
			http.Error(w, `{"error":"missing parentId"}`, http.StatusBadRequest)
			return
		}

		// Load parent task
		var parentType, parentIntent, parentResult, repoPath, commitSha string
		err := db.QueryRow(
			`SELECT type, COALESCE(intent, ''), COALESCE(result, ''), repo_path, COALESCE(commit_sha, '')
			 FROM claude_tasks WHERE id = ? AND status = 'completed'`,
			body.ParentID,
		).Scan(&parentType, &parentIntent, &parentResult, &repoPath, &commitSha)
		if err != nil {
			http.Error(w, `{"error":"completed parent task not found"}`, http.StatusNotFound)
			return
		}

		if checkUnpushed(w, repoPath) {
			return
		}

		// Default intent for spawned tasks
		intent := body.Intent
		if intent == "" {
			intent = "implementation"
		}

		// Build child prompt based on parent context
		var childPrompt string
		switch {
		case parentIntent == "new-feature":
			// ADR → implementation
			if body.CommitADR {
				childPrompt = fmt.Sprintf(
					"## Approved ADR\n\n"+
						"The following ADR has been approved. Commit it to `docs/` (follow the existing `ADR-NNNN-name.md` naming convention) as your first action before implementing.\n\n"+
						"```markdown\n%s\n```\n\n"+
						"## Instructions\n\nImplement the feature described in this ADR.",
					parentResult,
				)
			} else {
				childPrompt = fmt.Sprintf(
					"## Approved ADR\n\n"+
						"The following ADR has been approved for implementation. Do NOT commit the ADR itself to the repo — it is for context only.\n\n"+
						"%s\n\n"+
						"## Instructions\n\nImplement the feature described in this ADR.",
					parentResult,
				)
			}

		case parentType == "review":
			// Review findings → implementation
			shortSha := commitSha
			if len(shortSha) > 7 {
				shortSha = shortSha[:7]
			}
			childPrompt = fmt.Sprintf(
				"Address the following code review findings from commit %s.\n\n"+
					"## How to read these findings\n\n"+
					"- Each finding has a priority, location, issue description, and a step-by-step plan\n"+
					"- If a finding contains a `> User response:` blockquote, treat it as explicit guidance — follow it\n"+
					"- If a finding has multiple valid approaches and no user response, pick the cleanest one\n"+
					"- If a finding was removed from the review, the reviewer decided it's not applicable — skip it\n"+
					"- Only ask me if there is genuine ambiguity that requires a judgment call\n\n"+
					"## Review Findings\n\n%s",
				shortSha, parentResult,
			)

		default:
			// Generic: pass parent result as context
			childPrompt = fmt.Sprintf("## Context from parent task\n\n%s\n\n## Instructions\n\nImplement the changes described above.", parentResult)
		}

		// Create child task
		now := time.Now().Format(time.RFC3339)
		title := directiveTitle(childPrompt)
		res, err := db.Exec(
			`INSERT INTO claude_tasks (type, status, repo_path, commit_sha, prompt, title, intent, parent_id, created_at, started_at)
			 VALUES ('directive', 'draft', ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoPath, commitSha, childPrompt, title, intent, body.ParentID, now, now,
		)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		childID, _ := res.LastInsertId()
		id := int(childID)

		// Launch immediately
		launchRes, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:       id,
			Intent:       intent,
			UserPrompt:   childPrompt,
			RepoPath:     repoPath,
			WindowPrefix: intent,
		})
		if err != nil {
			log.Printf("cmdr: spawn from task %d failed: %v", body.ParentID, err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		enhanceTitle(db, bus, id, truncate(childPrompt, 500))

		log.Printf("cmdr: spawned task %d from parent %d (intent %q)", id, body.ParentID, intent)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      id,
			"target":  launchRes.Target,
			"session": launchRes.Session,
		})
	}
}

// --- Helpers ---

// checkUnpushed returns true (and writes a 409 response) if the repo has unpushed commits.
// Callers should return early when this returns true.
func checkUnpushed(w http.ResponseWriter, repoPath string) bool {
	if repoPath == "" {
		return false
	}
	ahead := unpushedCount(repoPath)
	if ahead == 0 {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(map[string]any{
		"error":    fmt.Sprintf("%d unpushed commit(s) on the current branch", ahead),
		"unpushed": ahead,
	})
	return true
}

// unpushedCount returns how many commits the current branch is ahead of its upstream.
// Returns 0 if there's no upstream or if the check fails.
func unpushedCount(repoPath string) int {
	out, err := exec.Command("git", "-C", repoPath, "rev-list", "--count", "@{u}..HEAD").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// extractTitle pulls a display title from the review result.
var headingRe = regexp.MustCompile(`(?m)^#{1,3}\s+(.+)$`)

func extractTitle(result string) string {
	var raw string
	if m := headingRe.FindStringSubmatch(result); len(m) > 1 {
		raw = m[1]
	} else {
		for _, line := range strings.SplitN(result, "\n", 10) {
			line = strings.TrimSpace(line)
			if line != "" {
				raw = line
				break
			}
		}
	}
	raw = strings.ReplaceAll(raw, "`", "")
	raw = strings.ReplaceAll(raw, "**", "")
	raw = strings.ReplaceAll(raw, "*", "")
	raw = strings.TrimSpace(raw)
	if len(raw) > 120 {
		raw = raw[:117] + "..."
	}
	return raw
}

var ollamaSem = make(chan struct{}, 1)

// enhanceTitle asynchronously replaces a task's heuristic title with an
// LLM-generated summary via Ollama. Fire-and-forget: failures are logged
// but never surfaced to the user.
func enhanceTitle(db *sql.DB, bus *EventBus, taskID int, content string) {
	go func() {
		ollamaSem <- struct{}{}
		defer func() { <-ollamaSem }()

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		title, err := ollama.Summarize(ctx, content)
		if err != nil {
			log.Printf("cmdr: ollama title for task %d failed: %v", taskID, err)
			return
		}

		db.Exec(`UPDATE claude_tasks SET title=? WHERE id=?`, title, taskID)
		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": taskID, "title": title,
		}})

		log.Printf("cmdr: task %d title enhanced: %s", taskID, title)
	}()
}

// truncate returns the first n characters of s, or all of s if shorter.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, "")
}

// resolveImageRefs rewrites markdown image syntax ![caption](/api/images/filename)
// to absolute file path references that Claude Code can read with its Read tool.
// e.g. ![sketch](/api/images/abc.png) → [image: ~/.cmdr/images/abc.png]
var markdownImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(/api/images/([\w.\-]+)\)`)

func resolveImageRefs(content string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return content
	}
	imgDir := filepath.Join(home, ".cmdr", "images")
	return markdownImageRe.ReplaceAllStringFunc(content, func(match string) string {
		parts := markdownImageRe.FindStringSubmatch(match)
		caption := parts[1]
		absPath := filepath.Join(imgDir, parts[2])
		if caption != "" {
			return fmt.Sprintf("[image (%s): %s]", caption, absPath)
		}
		return fmt.Sprintf("[image: %s]", absPath)
	})
}

// --- Worktree naming (user-namespaced) ---

// ghUser caches the GitHub username for branch namespacing.
var ghUser string

// getGHUser returns the cached GitHub username, fetching it once via `gh api user`.
func getGHUser() string {
	if ghUser != "" {
		return ghUser
	}
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return ""
	}
	ghUser = strings.TrimSpace(string(out))
	return ghUser
}

// buildWorktreeName returns a namespaced worktree/branch name: "<ghUser>/<prefix>-<taskID>".
// Falls back to "<prefix>-<taskID>" if the GitHub username can't be determined.
func buildWorktreeName(prefix string, taskID int) string {
	base := fmt.Sprintf("%s-%d", prefix, taskID)
	if user := getGHUser(); user != "" {
		return user + "/" + base
	}
	return base
}

// --- Worktree cleanup (unified) ---

// taskMarkerPath returns the marker file path for review-triggered refactors.
func taskMarkerPath(taskType, worktreeName string) string {
	if taskType != "directive" && worktreeName != "" {
		return filepath.Join(os.Getenv("HOME"), ".cmdr", "refactors", worktreeName)
	}
	return ""
}

// cleanupTaskWorktree removes the worktree (and marker file if applicable) for a single task.
func cleanupTaskWorktree(db *sql.DB, taskID int) {
	var repoPath, taskType, worktreeName, status string
	err := db.QueryRow(`SELECT repo_path, type, worktree, status FROM claude_tasks WHERE id = ?`, taskID).
		Scan(&repoPath, &taskType, &worktreeName, &status)
	if err != nil || worktreeName == "" {
		return
	}
	// Only clean up tasks that are in a worktree-using state
	if status != "completed" && status != "running" {
		return
	}

	removeWorktree(repoPath, taskID, worktreeName, taskMarkerPath(taskType, worktreeName))
}

// cleanupAllTaskWorktrees removes worktrees for all completed/resolved/refactoring tasks.
func cleanupAllTaskWorktrees(db *sql.DB) {
	rows, err := db.Query(`SELECT id, type, repo_path, worktree FROM claude_tasks WHERE worktree != '' AND status IN ('completed', 'running')`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var taskType, repoPath, worktreeName string
		if err := rows.Scan(&id, &taskType, &repoPath, &worktreeName); err != nil {
			continue
		}
		removeWorktree(repoPath, id, worktreeName, taskMarkerPath(taskType, worktreeName))
	}
}

// removeWorktree removes a git worktree and optional marker file.
func removeWorktree(repoPath string, taskID int, worktreeName, markerPath string) {
	worktreePath := filepath.Join(repoPath, ".claude", "worktrees", worktreeName)
	if _, err := os.Stat(worktreePath); err == nil {
		cmd := exec.Command("git", "-C", repoPath, "worktree", "remove", worktreePath, "--force")
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("cmdr: worktree remove failed (task %d): %s: %v", taskID, strings.TrimSpace(string(out)), err)
		} else {
			log.Printf("cmdr: pruned worktree %s (task %d)", worktreeName, taskID)
		}
	}
	if markerPath != "" {
		os.Remove(markerPath)
	}
}
