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
		query := `SELECT id, type, status, repo_path, commit_sha, COALESCE(title, ''), COALESCE(pr_url, ''), error_msg, created_at, started_at, completed_at, COALESCE(prompt, ''), COALESCE(intent, '')
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
			Snippet     string  `json:"snippet,omitempty"`
			prompt      string
		}

		var taskList []task
		for rows.Next() {
			var t task
			if err := rows.Scan(&t.ID, &t.Type, &t.Status, &t.RepoPath, &t.CommitSHA, &t.Title, &t.PRUrl,
				&t.ErrorMsg, &t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.prompt, &t.Intent); err != nil {
				continue
			}
			// Derive a short snippet from the prompt for frontend fallback title
			if t.Title == "" && t.prompt != "" {
				t.Snippet = directiveTitle(t.prompt)
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
				if (status == "running" || status == "refactoring" || status == "implementing") && !body.Force {
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
			res, err = db.Exec(`DELETE FROM claude_tasks WHERE status IN ('done', 'failed')`)
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
	var taskType, status string
	if err := db.QueryRow(`SELECT type, status FROM claude_tasks WHERE id = ?`, taskID).Scan(&taskType, &status); err != nil {
		return
	}
	windowName := taskWindowName(taskType, status, taskID)

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
		db.Exec(`UPDATE claude_tasks SET status='resolved', pr_url=?, completed_at=? WHERE id=?`,
			body.PRUrl, now, body.ID)

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": body.ID, "status": "resolved", "prUrl": body.PRUrl,
		}})

		log.Printf("cmdr: task %d resolved (PR: %s)", body.ID, body.PRUrl)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "resolved", "prUrl": body.PRUrl})
	}
}

// --- Task launch (config-driven) ---

// TaskLaunchConfig describes how to launch a Claude session in tmux.
type TaskLaunchConfig struct {
	TaskID         int
	Intent         string // optional intent ID for --append-system-prompt
	UserPrompt     string // the content to send to Claude
	RepoPath       string
	Session        string // explicit session name, or "" for auto-detect from repo
	WindowPrefix   string // e.g. "review", "task" → "review-42", "task-42"
	WorktreePrefix string // e.g. "refactor-review", "directive" → "refactor-review-42"
	MarkerDir      string // optional dir for writing task ID marker file (e.g. ~/.cmdr/refactors)
	RunningStatus  string // status to set on launch: "running" or "refactoring"
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

	// Write optional marker file
	var worktreeName string
	if cfg.WorktreePrefix != "" {
		worktreeName = fmt.Sprintf("%s-%d", cfg.WorktreePrefix, cfg.TaskID)
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
		if cfg.RunningStatus == "" || cfg.RunningStatus == "running" {
			if dp, err := prompts.GetDesignPrompt(cfg.Intent); err == nil && dp != "" {
				systemPrompt = dp
			}
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
		cmd = fmt.Sprintf("exec %s < '%s'", baseCmd, promptFile)
	}

	// Resolve session and create window
	var target string
	var sessionName string
	var err error

	if cfg.Session != "" {
		// Explicit session (e.g. "claude_refactor")
		target, err = tmux.CreateRefactorWindow(windowName, cfg.RepoPath, cmd)
		sessionName = cfg.Session
	} else {
		// Auto-detect session from repo path
		sessionName, err = findOrCreateSession(cfg.RepoPath)
		if err != nil {
			return TaskLaunchResult{}, fmt.Errorf("session: %w", err)
		}
		target, err = tmux.CreateDraftWindow(sessionName, windowName, cfg.RepoPath, cmd)
	}
	if err != nil {
		return TaskLaunchResult{}, fmt.Errorf("window: %w", err)
	}

	// Update task status
	status := cfg.RunningStatus
	if status == "" {
		status = "running"
	}
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE claude_tasks SET status=?, intent=?, started_at=? WHERE id=?`,
		status, cfg.Intent, now, cfg.TaskID)
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": cfg.TaskID, "status": status, "intent": cfg.Intent, "repoPath": cfg.RepoPath,
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
			Session:        "claude_refactor",
			WindowPrefix:   "review",
			WorktreePrefix: "refactor-review",
			MarkerDir:      filepath.Join(os.Getenv("HOME"), ".cmdr", "refactors"),
			RunningStatus:  "refactoring",
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
			WindowPrefix:   "impl",
			WorktreePrefix: "impl",
			RunningStatus:  "implementing",
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

// --- Worktree cleanup (unified) ---

// taskWorktreeInfo returns the worktree name and marker path for a task based on its type.
func taskWorktreeInfo(taskType, status string, taskID int) (worktreeName string, markerPath string) {
	// Delegations don't use worktrees
	if taskType == "delegation" {
		return
	}
	if taskType == "directive" && status == "implementing" {
		worktreeName = fmt.Sprintf("impl-%d", taskID)
		return
	}
	switch taskType {
	case "directive":
		worktreeName = fmt.Sprintf("directive-%d", taskID)
	default:
		// review-triggered refactors
		worktreeName = fmt.Sprintf("refactor-review-%d", taskID)
		markerPath = filepath.Join(os.Getenv("HOME"), ".cmdr", "refactors", worktreeName)
	}
	return
}

// cleanupTaskWorktree removes the worktree (and marker file if applicable) for a single task.
func cleanupTaskWorktree(db *sql.DB, taskID int) {
	var repoPath, taskType, status string
	err := db.QueryRow(`SELECT repo_path, type, status FROM claude_tasks WHERE id = ?`, taskID).
		Scan(&repoPath, &taskType, &status)
	if err != nil {
		return
	}
	// Only clean up tasks that are in a worktree-using state
	if status != "refactoring" && status != "implementing" && status != "resolved" && status != "completed" && status != "running" {
		return
	}

	worktreeName, markerPath := taskWorktreeInfo(taskType, status, taskID)
	removeWorktree(repoPath, taskID, worktreeName, markerPath)
}

// cleanupAllTaskWorktrees removes worktrees for all completed/resolved/refactoring tasks.
func cleanupAllTaskWorktrees(db *sql.DB) {
	rows, err := db.Query(`SELECT id, type, repo_path, status FROM claude_tasks WHERE status IN ('completed', 'resolved', 'refactoring', 'implementing')`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var taskType, repoPath, status string
		if err := rows.Scan(&id, &taskType, &repoPath, &status); err != nil {
			continue
		}
		worktreeName, markerPath := taskWorktreeInfo(taskType, status, id)
		removeWorktree(repoPath, id, worktreeName, markerPath)
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
