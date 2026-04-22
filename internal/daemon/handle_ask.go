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
	"strings"
	"sync"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
)

// --- Headless task runner (agent-agnostic with streaming) ---

// headlessProcesses tracks running agent processes by task ID for cancellation.
var headlessProcesses sync.Map // map[int]*exec.Cmd

// HeadlessConfig describes how to run a headless agent task.
type HeadlessConfig struct {
	TaskID       int
	Prompt       string
	WorkDir      string
	SystemPrompt string
	OutputFormat string // "markdown" (default), "html", or "text"
	PromptFile   string // if set, pipe prompt from this file via stdin instead of -p arg
}

// runHeadless runs a headless task using the default agent.
func runHeadless(db *sql.DB, bus *EventBus, cfg HeadlessConfig) {
	runHeadlessWithAgent(agt, db, bus, cfg)
}

// runHeadlessWithAgent runs a headless task using a specific agent.
func runHeadlessWithAgent(a agent.Agent, db *sql.DB, bus *EventBus, cfg HeadlessConfig) {
	ctx := context.Background()

	if a.Capabilities().Streaming {
		runHeadlessStreaming(a, ctx, db, bus, cfg)
	} else {
		runHeadlessSimple(a, ctx, db, bus, cfg)
	}
}

// runHeadlessStreaming runs with incremental event streaming.
func runHeadlessStreaming(a agent.Agent, ctx context.Context, db *sql.DB, bus *EventBus, cfg HeadlessConfig) {
	result, err := a.RunStreaming(ctx, agent.StreamingConfig{
		Prompt:       cfg.Prompt,
		WorkDir:      cfg.WorkDir,
		SystemPrompt: cfg.SystemPrompt,
		PromptFile:   cfg.PromptFile,
	}, func(evt agent.StreamEvent) {
		bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
			"id": cfg.TaskID, "type": evt.Type, "text": evt.Text, "tool": evt.Tool, "detail": evt.Detail,
		}})
	})

	if err != nil {
		failHeadless(db, bus, cfg.TaskID, err)
		return
	}

	// Store process handle for cancellation
	if result.Cmd != nil {
		headlessProcesses.Store(cfg.TaskID, result.Cmd)
		defer headlessProcesses.Delete(cfg.TaskID)
	}

	now := time.Now().Format(time.RFC3339)
	title := extractTitle(result.Output)
	outputFmt := cfg.OutputFormat
	if outputFmt == "" {
		outputFmt = "markdown"
	}
	db.Exec(`UPDATE agent_tasks SET status='resolved', result=?, title=?, agent_session_id=?, output_format=?, completed_at=? WHERE id=?`,
		result.Output, title, result.SessionID, outputFmt, now, cfg.TaskID)

	bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
		"id": cfg.TaskID, "type": "done",
	}})
	bus.Publish(Event{Type: "agent:task", Data: map[string]any{
		"id": cfg.TaskID, "status": "resolved", "title": title,
	}})

	enhanceTitle(db, bus, cfg.TaskID, truncate(result.Output, 1000))

	log.Printf("cmdr: headless task %d resolved (result ready)", cfg.TaskID)
}

// runHeadlessSimple runs without streaming — just final result.
func runHeadlessSimple(a agent.Agent, ctx context.Context, db *sql.DB, bus *EventBus, cfg HeadlessConfig) {
	out, err := a.RunSimple(ctx, agent.SimpleConfig{
		Prompt:  cfg.Prompt,
		WorkDir: cfg.WorkDir,
	})

	if err != nil {
		failHeadless(db, bus, cfg.TaskID, err)
		return
	}

	now := time.Now().Format(time.RFC3339)
	title := extractTitle(out)
	outputFmt := cfg.OutputFormat
	if outputFmt == "" {
		outputFmt = "markdown"
	}
	db.Exec(`UPDATE agent_tasks SET status='resolved', result=?, title=?, output_format=?, completed_at=? WHERE id=?`,
		out, title, outputFmt, now, cfg.TaskID)

	bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
		"id": cfg.TaskID, "type": "done",
	}})
	bus.Publish(Event{Type: "agent:task", Data: map[string]any{
		"id": cfg.TaskID, "status": "resolved", "title": title,
	}})

	enhanceTitle(db, bus, cfg.TaskID, truncate(out, 1000))

	log.Printf("cmdr: headless task %d resolved (result ready)", cfg.TaskID)
}

func failHeadless(db *sql.DB, bus *EventBus, taskID int, err error) {
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE agent_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`,
		err.Error(), now, taskID)
	bus.Publish(Event{Type: "agent:stream", Data: map[string]any{
		"id": taskID, "type": "error", "error": err.Error(),
	}})
	bus.Publish(Event{Type: "agent:task", Data: map[string]any{
		"id": taskID, "status": "failed",
	}})
	log.Printf("cmdr: headless task %d failed: %v", taskID, err)
}

// cancelHeadlessProcess kills the running agent process for a headless task.
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

	// Ask and review tasks → failed
	res, _ := db.Exec(`UPDATE agent_tasks SET status='failed', error_msg='daemon restarted', completed_at=?
		WHERE type IN ('ask', 'review') AND status = 'running'`, now)
	n, _ := res.RowsAffected()

	// Headless directive intents (e.g. analysis) → draft
	res2, _ := db.Exec(`UPDATE agent_tasks SET status='draft', error_msg='daemon restarted'
		WHERE type = 'directive' AND status = 'running' AND intent = 'analysis'`)
	n2, _ := res2.RowsAffected()

	if total := n + n2; total > 0 {
		log.Printf("cmdr: marked %d orphaned headless tasks as failed/draft", total)
	}
}

// --- Ask handler ---

// askSystemPromptFor returns the system prompt for ask tasks, adapting based
// on whether the /ask skill is available to consult the knowledge base.
func askSystemPromptFor(hasAskSkill bool) string {
	if hasAskSkill {
		return "Answer the question directly. If it seems like something the user may have personal notes on, use /ask to check their knowledge base. For general knowledge questions, just answer."
	}
	return "Answer the question directly and concisely."
}

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
			INSERT INTO agent_tasks (type, status, repo_path, prompt, title, created_at, started_at)
			VALUES ('ask', 'running', ?, ?, ?, ?, ?)
		`, askDir, body.Question, title, now, now)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		taskID, _ := res.LastInsertId()
		id := int(taskID)

		bus.Publish(Event{Type: "agent:task", Data: map[string]any{
			"id": id, "type": "ask", "status": "running", "title": title,
		}})

		go runHeadless(db, bus, HeadlessConfig{
			TaskID:       id,
			Prompt:       body.Question,
			WorkDir:      askDir,
			SystemPrompt: askSystemPromptFor(caps.AskSkill),
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "running"})
	}
}

// --- Continue in interactive session ---

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
		err := db.QueryRow(`SELECT type, COALESCE(agent_session_id, ''), COALESCE(repo_path, '') FROM agent_tasks WHERE id = ?`, body.ID).
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

		shellCmd, err := agt.ResumeCommand(sessionID)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		windowName := fmt.Sprintf("ask-%d", body.ID)

		// Directives resume in the repo session; asks use a dedicated session
		var target string
		if taskType == "directive" && repoPath != "" {
			sessionName, err := findOrCreateSession(repoPath)
			if err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
			target, err = term.CreateWindow(sessionName, windowName, resumeDir, shellCmd)
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

		// Store the terminal ref for lifecycle tracking
		db.Exec(`UPDATE agent_tasks SET terminal_target=? WHERE id=?`, target, body.ID)

		log.Printf("cmdr: task %d continued in %s (session %s)", body.ID, target, sessionID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": target})
	}
}

func createHeadlessWindow(windowName, dir, shellCmd string) (string, error) {
	sessionName, err := term.CreateSession(dir)
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	target, err := term.CreateWindow(sessionName, windowName, dir, shellCmd)
	if err != nil {
		return "", fmt.Errorf("creating window: %w", err)
	}
	return target, nil
}

// --- Helpers ---

func askTitle(question string) string {
	t := strings.TrimSpace(question)
	if len(t) > 80 {
		t = t[:77] + "..."
	}
	return t
}
