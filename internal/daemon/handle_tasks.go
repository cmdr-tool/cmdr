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
	"sync"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/prompts"
)

// --- Task CRUD ---

func handleListAgentTasks(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := `SELECT id, type, status, repo_path, commit_sha, COALESCE(title, ''), COALESCE(pr_url, ''), error_msg, created_at, started_at, completed_at, COALESCE(prompt, ''), COALESCE(intent, ''), parent_id, COALESCE(output_format, 'markdown')
			FROM agent_tasks ORDER BY created_at DESC LIMIT 50`
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
			Headless     bool    `json:"headless,omitempty"`
			OutputFormat string  `json:"outputFormat,omitempty"`
			prompt       string
		}

		var taskList []task
		for rows.Next() {
			var t task
			if err := rows.Scan(&t.ID, &t.Type, &t.Status, &t.RepoPath, &t.CommitSHA, &t.Title, &t.PRUrl,
				&t.ErrorMsg, &t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.prompt, &t.Intent, &t.ParentID, &t.OutputFormat); err != nil {
				continue
			}
			t.Headless = t.Type == "ask" || t.Type == "review" || prompts.IntentIsHeadless(t.Intent)
			taskList = append(taskList, t)
		}
		if taskList == nil {
			taskList = []task{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(taskList)
	}
}

func handleGetAgentTaskResult(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		var result, prompt, status, errMsg, intent string
		err := db.QueryRow(`SELECT result, prompt, status, error_msg, COALESCE(intent, '') FROM agent_tasks WHERE id = ?`, id).
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

func handleUpdateAgentTaskResult(db *sql.DB, bus *EventBus) http.HandlerFunc {
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
		db.Exec(`UPDATE agent_tasks SET result=?, title=? WHERE id=?`, body.Result, title, body.ID)

		enhanceTitle(db, bus, body.ID, truncate(body.Result, 1000))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleDismissAgentTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
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
			if err := db.QueryRow(`SELECT status FROM agent_tasks WHERE id = ?`, body.ID).Scan(&status); err == nil {
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

		// Collect cleanup info before deleting rows from the DB.
		// Actual cleanup (git worktree remove, tmux kill-window) runs
		// asynchronously so the DELETE + response aren't blocked by
		// slow shell-outs — which caused tasks to reappear on refresh.
		type cleanupInfo struct {
			id           int
			repoPath     string
			worktreeName string
			taskType     string
			intent       string
		}
		var cleanups []cleanupInfo

		if body.ID > 0 {
			var ci cleanupInfo
			ci.id = body.ID
			err := db.QueryRow(
				`SELECT repo_path, COALESCE(worktree, ''), type, COALESCE(intent, '') FROM agent_tasks WHERE id = ?`,
				body.ID,
			).Scan(&ci.repoPath, &ci.worktreeName, &ci.taskType, &ci.intent)
			if err == nil {
				cleanups = append(cleanups, ci)
			}
		} else if body.All == "completed" {
			rows, err := db.Query(
				`SELECT id, repo_path, COALESCE(worktree, ''), type, COALESCE(intent, '')
				 FROM agent_tasks WHERE type != 'delegation' AND status IN ('failed', 'completed')`,
			)
			if err == nil {
				for rows.Next() {
					var ci cleanupInfo
					if rows.Scan(&ci.id, &ci.repoPath, &ci.worktreeName, &ci.taskType, &ci.intent) == nil {
						cleanups = append(cleanups, ci)
					}
				}
				rows.Close()
			}
		}

		// DELETE first so the response reflects the new state immediately.
		var res sql.Result
		var err error
		if body.All == "completed" {
			// Terminal = failed or completed. Resolved tasks need user action.
			res, err = db.Exec(`
				DELETE FROM agent_tasks
				WHERE type != 'delegation'
				  AND status IN ('failed', 'completed')
			`)
		} else if body.ID > 0 {
			res, err = db.Exec(`DELETE FROM agent_tasks WHERE id = ?`, body.ID)
		} else {
			http.Error(w, `{"error":"missing id or all"}`, http.StatusBadRequest)
			return
		}

		// Async cleanup: kill windows and remove worktrees in the background.
		go func() {
			for _, ci := range cleanups {
				windowName := taskWindowName(ci.taskType, ci.intent, ci.id)
				sessions, sErr := term.ListSessions()
				if sErr == nil {
					for _, s := range sessions {
						for _, w := range s.Windows {
							if w.Name == windowName {
								target := fmt.Sprintf("%s:%s", s.Name, w.Name)
								term.KillWindow(target)
								log.Printf("cmdr: killed task window %s (task %d)", target, ci.id)
							}
						}
					}
				}
				if ci.worktreeName != "" {
					removeWorktree(ci.repoPath, ci.id, ci.worktreeName)
				}
			}
		}()

		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()

		if body.ID > 0 && n > 0 {
			bus.Publish(Event{Type: "agent:task", Data: map[string]any{
				"id": body.ID, "status": "dismissed",
			}})
		} else if body.All == "completed" && n > 0 {
			bus.Publish(Event{Type: "agent:task", Data: map[string]any{
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
	if err := db.QueryRow(`SELECT type, COALESCE(intent, '') FROM agent_tasks WHERE id = ?`, taskID).Scan(&taskType, &intent); err != nil {
		return
	}
	windowName := taskWindowName(taskType, intent, taskID)

	// Find the window across all sessions and kill it
	sessions, err := term.ListSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		for _, w := range s.Windows {
			if w.Name == windowName {
				target := fmt.Sprintf("%s:%s", s.Name, w.Name)
				term.KillWindow(target)
				log.Printf("cmdr: killed task window %s (task %d)", target, taskID)
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
		err := db.QueryRow(`SELECT type, status, COALESCE(intent, '') FROM agent_tasks WHERE id = ?`, body.ID).Scan(&taskType, &status, &intent)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		if status != "running" {
			http.Error(w, `{"error":"task is not running"}`, http.StatusConflict)
			return
		}

		// Headless tasks (ask, review, headless directives): kill process, mark cancelled
		if taskType == "ask" || taskType == "review" || prompts.IntentIsHeadless(intent) {
			cancelHeadlessProcess(body.ID)
			now := time.Now().Format(time.RFC3339)
			if taskType == "directive" {
				// Headless directives reset to draft like interactive ones
				db.Exec(`UPDATE agent_tasks SET status='draft', intent='', worktree='', started_at=NULL, completed_at=NULL, result='', error_msg='', pr_url='' WHERE id=?`, body.ID)
				bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
					"id": body.ID, "type": "done",
				}})
				bus.Publish(Event{Type: "agent:task", Data: map[string]any{
					"id": body.ID, "status": "draft",
				}})
				log.Printf("cmdr: headless directive %d cancelled, restored to draft", body.ID)
			} else {
				db.Exec(`UPDATE agent_tasks SET status='failed', error_msg='cancelled', completed_at=? WHERE id=?`, now, body.ID)
				bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
					"id": body.ID, "type": "done",
				}})
				bus.Publish(Event{Type: "agent:task", Data: map[string]any{
					"id": body.ID, "status": "failed",
				}})
				log.Printf("cmdr: ask %d cancelled", body.ID)
			}
		} else if taskType == "directive" {
			// Interactive directives: kill tmux window, reset to draft
			killTaskWindow(db, body.ID)
			cleanupTaskWorktree(db, body.ID)
			db.Exec(`UPDATE agent_tasks SET status='draft', intent='', worktree='', started_at=NULL, completed_at=NULL, result='', error_msg='', pr_url='' WHERE id=?`, body.ID)
			bus.Publish(Event{Type: "agent:task", Data: map[string]any{
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
		db.Exec(`UPDATE agent_tasks SET status='resolved', pr_url=?, completed_at=? WHERE id=?`,
			body.PRUrl, now, body.ID)

		bus.Publish(Event{Type: "agent:task", Data: map[string]any{
			"id": body.ID, "status": "resolved", "prUrl": body.PRUrl,
		}})

		log.Printf("cmdr: task %d resolved (PR: %s)", body.ID, body.PRUrl)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "resolved", "prUrl": body.PRUrl})
	}
}

func handleRestoreTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID int `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		var status string
		if err := db.QueryRow(`SELECT status FROM agent_tasks WHERE id = ?`, body.ID).Scan(&status); err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		if status != "failed" && status != "completed" {
			http.Error(w, `{"error":"can only restore failed or completed tasks"}`, http.StatusConflict)
			return
		}

		db.Exec(`UPDATE agent_tasks SET status='draft', worktree='', started_at=NULL, completed_at=NULL, result='', error_msg='', pr_url='' WHERE id=?`, body.ID)

		bus.Publish(Event{Type: "agent:task", Data: map[string]any{
			"id": body.ID, "status": "draft",
		}})

		log.Printf("cmdr: task %d restored to draft (intent preserved)", body.ID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "draft"})
	}
}

// --- Task launch (config-driven) ---

// TaskLaunchConfig describes how to launch a Claude session.
type TaskLaunchConfig struct {
	TaskID         int
	Intent         string // optional intent ID for --append-system-prompt
	UserPrompt     string // the content to send to Claude
	RepoPath       string
	WindowPrefix   string // e.g. "refactor", "task" → "refactor-42", "task-42"
	WorktreePrefix string // overrides WindowPrefix for worktree naming; defaults to WindowPrefix if empty
}

// TaskLaunchResult is returned from launchTask with session/window info.
type TaskLaunchResult struct {
	Target  string // terminal target "session:window"
	Session string
	Window  string
}

// launchTask launches a Claude session based on the given config.
func launchTask(db *sql.DB, bus *EventBus, cfg TaskLaunchConfig) (TaskLaunchResult, error) {
	windowName := fmt.Sprintf("%s-%d", cfg.WindowPrefix, cfg.TaskID)

	// Only create a worktree if the intent metadata says so
	var worktreeName string
	meta := prompts.GetIntentMeta(cfg.Intent)
	if meta.Worktree && agt.Capabilities().Worktrees {
		prefix := cfg.WorktreePrefix
		if prefix == "" {
			prefix = cfg.WindowPrefix
		}
		if prefix != "" {
			worktreeName = buildWorktreeName(prefix, cfg.TaskID)
		}
	}

	// Resolve image references to absolute paths Claude can read
	prompt := resolveImageRefs(cfg.UserPrompt)

	// Write prompt to a temp file to avoid command length limits.
	// Terminal multiplexers may have char limits for new-window commands; ADRs can be 20K+.
	promptDir := filepath.Join(os.TempDir(), "cmdr")
	os.MkdirAll(promptDir, 0o700)
	promptFile := filepath.Join(promptDir, fmt.Sprintf("task-%d-prompt.md", cfg.TaskID))
	os.WriteFile(promptFile, []byte(prompt), 0o644)

	// Resolve system prompt from intent
	var systemPrompt string
	if cfg.Intent != "" {
		if dp, err := prompts.GetDesignPrompt(cfg.Intent); err == nil && dp != "" {
			systemPrompt = dp
		}
		if systemPrompt == "" {
			if ip, err := prompts.GetIntentPrompt(cfg.Intent); err == nil {
				systemPrompt = ip
			}
		}
	} else {
		if gp, err := prompts.GetIntentPrompt("generic"); err == nil {
			systemPrompt = gp
		}
	}

	// Build agent command via adapter — handles binary, flags, worktree support
	wt := worktreeName
	if !agt.Capabilities().Worktrees {
		wt = ""
	}
	cmd, err := agt.InteractiveCommand(agent.InteractiveConfig{
		WorktreeName: wt,
		TaskName:     fmt.Sprintf("cmdr-task-%d", cfg.TaskID),
		SystemPrompt: systemPrompt,
		PromptFile:   promptFile,
	})
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("agent command: %w", err)
	}

	// Resolve session and create window
	sessionName, err := findOrCreateSession(cfg.RepoPath)
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("session: %w", err)
	}
	target, err := term.CreateWindow(sessionName, windowName, cfg.RepoPath, cmd)
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("window: %w", err)
	}

	// Update task status
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE agent_tasks SET status='running', intent=?, worktree=?, terminal_target=?, started_at=? WHERE id=?`,
		cfg.Intent, worktreeName, target, now, cfg.TaskID)
	bus.Publish(Event{Type: "agent:task", Data: map[string]any{
		"id": cfg.TaskID, "status": "running", "intent": cfg.Intent, "repoPath": cfg.RepoPath,
	}})

	log.Printf("cmdr: task %d launched (session %s, target %s, intent %q)", cfg.TaskID, sessionName, target, cfg.Intent)

	return TaskLaunchResult{Target: target, Session: sessionName, Window: windowName}, nil
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
			 FROM agent_tasks WHERE id = ? AND status = 'resolved'`,
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
			`INSERT INTO agent_tasks (type, status, repo_path, commit_sha, prompt, title, intent, parent_id, created_at, started_at)
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

		// Mark parent task as completed first — this is the critical,
		// non-retriable lifecycle transition. Everything else can be
		// retried or is best-effort.
		for range 3 {
			if _, err := db.Exec(`UPDATE agent_tasks SET status='completed' WHERE id = ?`, body.ParentID); err != nil {
				log.Printf("cmdr: retrying parent %d status update: %v", body.ParentID, err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			break
		}
		bus.Publish(Event{Type: "agent:task", Data: map[string]any{
			"id": body.ParentID, "status": "completed",
		}})

		// Best-effort: enhance child title, clean up parent window/worktree
		enhanceTitle(db, bus, id, truncate(parentResult, 500))
		killTaskWindow(db, body.ParentID)
		cleanupTaskWorktree(db, body.ParentID)

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

var (
	summarizeSem = make(chan struct{}, 3)

	// Per-task debounce: each new enhanceTitle call cancels any pending one
	// for the same task, so rapid saves only produce one summarization.
	titleMu   sync.Mutex
	titleGens = map[int]int64{} // task ID → generation counter
)

// enhanceTitle asynchronously replaces a task's heuristic title with a
// summarizer-generated title. Fire-and-forget: failures are logged
// but never surfaced to the user. Debounced per task — rapid calls
// cancel previous pending summarizations.
func enhanceTitle(db *sql.DB, bus *EventBus, taskID int, content string) {
	if sum == nil {
		return
	}

	// Increment generation — any in-flight summarization with an older
	// generation will discard its result when it finishes.
	titleMu.Lock()
	titleGens[taskID]++
	gen := titleGens[taskID]
	titleMu.Unlock()

	go func() {
		defer func() {
			titleMu.Lock()
			if titleGens[taskID] == gen {
				delete(titleGens, taskID)
			}
			titleMu.Unlock()
		}()

		// Debounce: wait briefly so rapid saves collapse into one call.
		// If a newer call arrives during this window, this one bails out.
		time.Sleep(2 * time.Second)
		titleMu.Lock()
		stale := titleGens[taskID] != gen
		titleMu.Unlock()
		if stale {
			return
		}

		summarizeSem <- struct{}{}
		defer func() { <-summarizeSem }()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		title, err := sum.Summarize(ctx, content)
		if err != nil {
			log.Printf("cmdr: title for task %d failed: %v", taskID, err)
			return
		}

		db.Exec(`UPDATE agent_tasks SET title=? WHERE id=?`, title, taskID)
		bus.Publish(Event{Type: "agent:task", Data: map[string]any{
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

// --- Worktree cleanup ---

// cleanupTaskWorktree removes the worktree for a single task.
func cleanupTaskWorktree(db *sql.DB, taskID int) {
	var repoPath, worktreeName, status string
	err := db.QueryRow(`SELECT repo_path, worktree, status FROM agent_tasks WHERE id = ?`, taskID).
		Scan(&repoPath, &worktreeName, &status)
	if err != nil || worktreeName == "" {
		return
	}
	if status != "completed" && status != "running" {
		return
	}
	removeWorktree(repoPath, taskID, worktreeName)
}

// worktreeDir resolves the actual worktree directory name. Claude Code
// sanitizes worktree names by replacing "/" with "+" in directory names,
// so "mikehu/feature-1" becomes "mikehu+feature-1" on disk. We try the
// sanitized form first (most common), then fall back to the raw name.
func worktreeDir(repoPath, worktreeName string) string {
	sanitized := strings.ReplaceAll(worktreeName, "/", "+")
	candidate := filepath.Join(repoPath, ".claude", "worktrees", sanitized)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return filepath.Join(repoPath, ".claude", "worktrees", worktreeName)
}

// removeWorktree removes a git worktree.
func removeWorktree(repoPath string, taskID int, worktreeName string) {
	worktreePath := worktreeDir(repoPath, worktreeName)
	if _, err := os.Stat(worktreePath); err == nil {
		cmd := exec.Command("git", "-C", repoPath, "worktree", "remove", worktreePath, "--force")
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("cmdr: worktree remove failed (task %d): %s: %v", taskID, strings.TrimSpace(string(out)), err)
		} else {
			log.Printf("cmdr: pruned worktree %s (task %d)", worktreeName, taskID)
		}
	}
}
