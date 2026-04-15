package daemon

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mikehu/cmdr/internal/tmux"
)

// --- Headless task runner (claude -p with streaming) ---

// headlessProcesses tracks running headless claude processes by task ID for cancellation.
var headlessProcesses sync.Map // map[int]*exec.Cmd

// HeadlessConfig describes how to run a headless claude -p task.
type HeadlessConfig struct {
	TaskID       int
	Prompt       string
	WorkDir      string
	SystemPrompt string // optional --append-system-prompt
}

// runHeadless runs claude -p with streaming, publishing progress via SSE.
// Used by both ask tasks and headless directive intents (e.g. analysis).
func runHeadless(db *sql.DB, bus *EventBus, cfg HeadlessConfig) {
	args := []string{"-p", cfg.Prompt, "--output-format", "stream-json", "--verbose"}
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = cfg.WorkDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		failHeadless(db, bus, cfg.TaskID, err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		failHeadless(db, bus, cfg.TaskID, err)
		return
	}

	headlessProcesses.Store(cfg.TaskID, cmd)
	defer headlessProcesses.Delete(cfg.TaskID)

	log.Printf("cmdr: headless task %d started (pid %d)", cfg.TaskID, cmd.Process.Pid)

	var finalResult, sessionID string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}

		evtType, _ := evt["type"].(string)

		switch evtType {
		case "assistant":
			msg, _ := evt["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch b["type"] {
				case "text":
					if text, ok := b["text"].(string); ok && text != "" {
						bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
							"id": cfg.TaskID, "type": "text", "text": text,
						}})
					}
				case "tool_use":
					name, _ := b["name"].(string)
					if name != "" {
						detail := toolDetail(name, b["input"])
						bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
							"id": cfg.TaskID, "type": "tool", "tool": name, "detail": detail,
						}})
					}
				}
			}

		case "result":
			if r, ok := evt["result"].(string); ok {
				finalResult = r
			}
			if sid, ok := evt["session_id"].(string); ok {
				sessionID = sid
			}
		}
	}

	if err := cmd.Wait(); err != nil && finalResult == "" {
		failHeadless(db, bus, cfg.TaskID, fmt.Errorf("claude exited: %w", err))
		return
	}

	if finalResult == "" {
		failHeadless(db, bus, cfg.TaskID, fmt.Errorf("no result from claude"))
		return
	}

	now := time.Now().Format(time.RFC3339)
	title := extractTitle(finalResult)
	db.Exec(`UPDATE claude_tasks SET status='completed', result=?, title=?, claude_session_id=?, completed_at=? WHERE id=?`,
		finalResult, title, sessionID, now, cfg.TaskID)

	bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
		"id": cfg.TaskID, "type": "done",
	}})
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": cfg.TaskID, "status": "completed", "title": title,
	}})

	enhanceTitle(db, bus, cfg.TaskID, truncate(finalResult, 1000))

	log.Printf("cmdr: headless task %d completed", cfg.TaskID)
}

func failHeadless(db *sql.DB, bus *EventBus, taskID int, err error) {
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE claude_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`,
		err.Error(), now, taskID)
	bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
		"id": taskID, "type": "error", "error": err.Error(),
	}})
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": taskID, "status": "failed",
	}})
	log.Printf("cmdr: headless task %d failed: %v", taskID, err)
}

// cancelHeadlessProcess kills the running claude process for a headless task.
func cancelHeadlessProcess(taskID int) bool {
	v, ok := headlessProcesses.LoadAndDelete(taskID)
	if !ok {
		return false
	}
	cmd := v.(*exec.Cmd)
	if cmd.Process != nil {
		cmd.Process.Kill()
		log.Printf("cmdr: headless task %d process killed (pid %d)", taskID, cmd.Process.Pid)
		return true
	}
	return false
}

// cleanupOrphanedHeadlessTasks marks any headless tasks left running from a
// previous daemon instance as failed, since the goroutine reading their output is gone.
func cleanupOrphanedHeadlessTasks(db *sql.DB) {
	now := time.Now().Format(time.RFC3339)

	// Ask tasks
	res, _ := db.Exec(`UPDATE claude_tasks SET status='failed', error_msg='daemon restarted', completed_at=?
		WHERE type = 'ask' AND status = 'running'`, now)
	n, _ := res.RowsAffected()

	// Headless directive intents (e.g. analysis)
	res2, _ := db.Exec(`UPDATE claude_tasks SET status='draft', error_msg='daemon restarted'
		WHERE type = 'directive' AND status = 'running' AND intent = 'analysis'`, )
	n2, _ := res2.RowsAffected()

	if total := n + n2; total > 0 {
		log.Printf("cmdr: marked %d orphaned headless tasks as failed/draft", total)
	}
}

// --- Ask handler ---

const askSystemPrompt = "Answer the question directly. If it seems like something the user may have personal notes on, use /ask to check their knowledge base. For general knowledge questions, just answer."

func handleAsk(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Question string `json:"question"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Question) == "" {
			http.Error(w, `{"error":"missing question"}`, http.StatusBadRequest)
			return
		}

		home, _ := os.UserHomeDir()
		askDir := filepath.Join(home, ".cmdr")
		os.MkdirAll(askDir, 0o700)

		now := time.Now().Format(time.RFC3339)
		title := askTitle(body.Question)
		res, err := db.Exec(`
			INSERT INTO claude_tasks (type, status, repo_path, prompt, title, created_at, started_at)
			VALUES ('ask', 'running', ?, ?, ?, ?, ?)
		`, askDir, body.Question, title, now, now)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		taskID, _ := res.LastInsertId()
		id := int(taskID)

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": id, "type": "ask", "status": "running", "title": title,
		}})

		go runHeadless(db, bus, HeadlessConfig{
			TaskID:       id,
			Prompt:       body.Question,
			WorkDir:      askDir,
			SystemPrompt: askSystemPrompt,
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "running"})
	}
}

// --- Continue in interactive session ---

const askSessionName = "ask_claude"

func handleContinueSession(db *sql.DB) http.HandlerFunc {
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

		var taskType, sessionID, repoPath string
		err := db.QueryRow(`SELECT type, COALESCE(claude_session_id, ''), COALESCE(repo_path, '') FROM claude_tasks WHERE id = ?`, body.ID).
			Scan(&taskType, &sessionID, &repoPath)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		if sessionID == "" {
			http.Error(w, `{"error":"no session to resume"}`, http.StatusBadRequest)
			return
		}

		// Use the directory where the session was originally created
		resumeDir := repoPath
		if resumeDir == "" {
			home, _ := os.UserHomeDir()
			resumeDir = filepath.Join(home, ".cmdr")
		}

		shellCmd := fmt.Sprintf("exec claude --resume '%s'", sessionID)
		windowName := fmt.Sprintf("ask-%d", body.ID)

		// Directives resume in the repo's tmux session; asks use a dedicated session
		var target string
		if taskType == "directive" && repoPath != "" {
			tmuxSession, err := findOrCreateSession(repoPath)
			if err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
			target, err = tmux.CreateDraftWindow(tmuxSession, windowName, resumeDir, shellCmd)
			if err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
		} else {
			var err error
			target, err = createHeadlessWindow(windowName, resumeDir, shellCmd)
			if err != nil {
				log.Printf("cmdr: continue task %d failed: %v", body.ID, err)
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
		}

		log.Printf("cmdr: task %d continued in %s (session %s)", body.ID, target, sessionID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": target})
	}
}

func createHeadlessWindow(windowName, dir, shellCmd string) (string, error) {
	args := []string{"bash", "-c", shellCmd}

	if err := exec.Command("tmux", "has-session", "-t="+askSessionName).Run(); err != nil {
		cmdArgs := append([]string{"new-session", "-ds", askSessionName, "-n", windowName, "-c", dir}, args...)
		if out, err := exec.Command("tmux", cmdArgs...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
		}
	} else {
		cmdArgs := append([]string{"new-window", "-t", askSessionName, "-n", windowName, "-c", dir}, args...)
		if out, err := exec.Command("tmux", cmdArgs...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("tmux new-window: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	// Keep window alive if claude exits with an error so the user can see what happened
	target := askSessionName + ":" + windowName
	exec.Command("tmux", "set-option", "-t", target, "remain-on-exit", "on").Run()

	exec.Command("tmux", "switch-client", "-t", askSessionName).Run()
	return target, nil
}

// --- Helpers ---

func toolDetail(name string, input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	switch name {
	case "Read":
		if p, ok := m["file_path"].(string); ok {
			if i := strings.Index(p, "ThoughtQuarry/"); i >= 0 {
				return p[i+len("ThoughtQuarry/"):]
			}
			return p
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

func askTitle(question string) string {
	t := strings.TrimSpace(question)
	if len(t) > 80 {
		t = t[:77] + "..."
	}
	return t
}
