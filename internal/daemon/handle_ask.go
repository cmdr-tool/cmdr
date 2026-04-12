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
	"strings"
	"time"
)

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

		now := time.Now().Format(time.RFC3339)
		title := askTitle(body.Question)
		res, err := db.Exec(`
			INSERT INTO claude_tasks (type, status, repo_path, prompt, title, created_at, started_at)
			VALUES ('ask', 'running', '', ?, ?, ?, ?)
		`, body.Question, title, now, now)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		taskID, _ := res.LastInsertId()
		id := int(taskID)

		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": id, "type": "ask", "status": "running", "title": title,
		}})

		go runAsk(db, bus, id, body.Question)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "running"})
	}
}

func runAsk(db *sql.DB, bus *EventBus, taskID int, question string) {
	home, _ := os.UserHomeDir()

	cmd := exec.Command("claude", "-p", "/ask "+question, "--output-format", "stream-json", "--verbose")
	cmd.Dir = home

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		failAsk(db, bus, taskID, err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		failAsk(db, bus, taskID, err)
		return
	}

	log.Printf("cmdr: ask %d started (pid %d): %s", taskID, cmd.Process.Pid, question)

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
							"id": taskID, "type": "text", "text": text,
						}})
					}
				case "tool_use":
					name, _ := b["name"].(string)
					if name != "" {
						detail := toolDetail(name, b["input"])
						bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
							"id": taskID, "type": "tool", "tool": name, "detail": detail,
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
		failAsk(db, bus, taskID, fmt.Errorf("claude exited: %w", err))
		return
	}

	if finalResult == "" {
		failAsk(db, bus, taskID, fmt.Errorf("no result from claude"))
		return
	}

	now := time.Now().Format(time.RFC3339)
	title := extractTitle(finalResult)
	db.Exec(`UPDATE claude_tasks SET status='completed', result=?, title=?, claude_session_id=?, completed_at=? WHERE id=?`,
		finalResult, title, sessionID, now, taskID)

	bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
		"id": taskID, "type": "done",
	}})
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": taskID, "status": "completed", "title": title,
	}})

	enhanceTitle(db, bus, taskID, truncate(finalResult, 1000))

	log.Printf("cmdr: ask %d completed", taskID)
}

func failAsk(db *sql.DB, bus *EventBus, taskID int, err error) {
	now := time.Now().Format(time.RFC3339)
	db.Exec(`UPDATE claude_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`,
		err.Error(), now, taskID)
	bus.Publish(Event{Type: "claude:ask:stream", Data: map[string]any{
		"id": taskID, "type": "error", "error": err.Error(),
	}})
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": taskID, "status": "failed",
	}})
	log.Printf("cmdr: ask %d failed: %v", taskID, err)
}

// cleanupOrphanedAskTasks marks any ask tasks left running from a previous
// daemon instance as failed, since the goroutine reading their output is gone.
func cleanupOrphanedAskTasks(db *sql.DB) {
	res, _ := db.Exec(`UPDATE claude_tasks SET status='failed', error_msg='daemon restarted', completed_at=?
		WHERE type = 'ask' AND status = 'running'`, time.Now().Format(time.RFC3339))
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("cmdr: marked %d orphaned ask tasks as failed", n)
	}
}

// --- Tool detail + title helpers ---

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

// --- Continue in interactive session ---

const askSession = "ask_claude"

func handleContinueAsk(db *sql.DB) http.HandlerFunc {
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

		var sessionID string
		err := db.QueryRow(`SELECT COALESCE(claude_session_id, '') FROM claude_tasks WHERE id = ?`, body.ID).
			Scan(&sessionID)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		if sessionID == "" {
			http.Error(w, `{"error":"no session to resume"}`, http.StatusBadRequest)
			return
		}

		home, _ := os.UserHomeDir()
		shellCmd := fmt.Sprintf("claude --resume '%s'", sessionID)
		windowName := fmt.Sprintf("ask-%d", body.ID)

		target, err := createAskWindow(windowName, home, shellCmd)
		if err != nil {
			log.Printf("cmdr: continue ask %d failed: %v", body.ID, err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		log.Printf("cmdr: ask %d continued in %s (session %s)", body.ID, target, sessionID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": target})
	}
}

func createAskWindow(windowName, dir, shellCmd string) (string, error) {
	args := []string{"bash", "-c", shellCmd}

	if err := exec.Command("tmux", "has-session", "-t="+askSession).Run(); err != nil {
		cmdArgs := append([]string{"new-session", "-ds", askSession, "-n", windowName, "-c", dir}, args...)
		if out, err := exec.Command("tmux", cmdArgs...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
		}
	} else {
		cmdArgs := append([]string{"new-window", "-t", askSession, "-n", windowName, "-c", dir}, args...)
		if out, err := exec.Command("tmux", cmdArgs...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("tmux new-window: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	exec.Command("tmux", "switch-client", "-t", askSession).Run()
	return askSession + ":" + windowName, nil
}
