package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/scheduler"
)

// --- Agentic task CRUD + execution ---

type agenticTaskRow struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	Prompt     string  `json:"prompt"`
	Schedule   string  `json:"schedule"`
	Enabled    bool    `json:"enabled"`
	WorkingDir string  `json:"working_dir"`
	LastRunAt  *string `json:"last_run_at"`
	LastResult string  `json:"last_result"`
	LastStatus string  `json:"last_status"`
	CreatedAt  string  `json:"created_at"`
}

func handleListAgenticTasks(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT id, name, prompt, schedule, enabled, working_dir,
			last_run_at, last_result, last_status, created_at
			FROM agentic_tasks ORDER BY name`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		items := make([]agenticTaskRow, 0)
		for rows.Next() {
			var t agenticTaskRow
			var enabled int
			if err := rows.Scan(&t.ID, &t.Name, &t.Prompt, &t.Schedule, &enabled,
				&t.WorkingDir, &t.LastRunAt, &t.LastResult, &t.LastStatus, &t.CreatedAt); err != nil {
				continue
			}
			t.Enabled = enabled == 1
			items = append(items, t)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

func handleCreateAgenticTask(db *sql.DB, bus *EventBus, s *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Name       string `json:"name"`
			Prompt     string `json:"prompt"`
			Schedule   string `json:"schedule"`
			Enabled    bool   `json:"enabled"`
			WorkingDir string `json:"working_dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		body.Prompt = strings.TrimSpace(body.Prompt)
		body.Schedule = strings.TrimSpace(body.Schedule)

		if body.Name == "" || body.Prompt == "" || body.Schedule == "" {
			http.Error(w, `{"error":"name, prompt, and schedule are required"}`, http.StatusBadRequest)
			return
		}

		now := time.Now().Format(time.RFC3339)
		enabled := 0
		if body.Enabled {
			enabled = 1
		}

		res, err := db.Exec(`INSERT INTO agentic_tasks (name, prompt, schedule, enabled, working_dir, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			body.Name, body.Prompt, body.Schedule, enabled, body.WorkingDir, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				http.Error(w, `{"error":"task name already exists"}`, http.StatusConflict)
				return
			}
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		id, _ := res.LastInsertId()

		if body.Enabled {
			scheduleAgenticTask(s, db, bus, int(id), body.Name, body.Prompt, body.Schedule, body.WorkingDir)
		}

		bus.Publish(Event{Type: "agentic:update", Data: map[string]any{"action": "created", "id": id}})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   id,
			"name": body.Name,
		})
	}
}

func handleUpdateAgenticTask(db *sql.DB, bus *EventBus, s *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Prompt     string `json:"prompt"`
			Schedule   string `json:"schedule"`
			Enabled    bool   `json:"enabled"`
			WorkingDir string `json:"working_dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}
		if body.ID == 0 {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		body.Name = strings.TrimSpace(body.Name)
		body.Prompt = strings.TrimSpace(body.Prompt)
		body.Schedule = strings.TrimSpace(body.Schedule)

		if body.Name == "" || body.Prompt == "" || body.Schedule == "" {
			http.Error(w, `{"error":"name, prompt, and schedule are required"}`, http.StatusBadRequest)
			return
		}

		// Look up old name so we can remove the old cron entry
		var oldName string
		if err := db.QueryRow(`SELECT name FROM agentic_tasks WHERE id = ?`, body.ID).Scan(&oldName); err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		enabled := 0
		if body.Enabled {
			enabled = 1
		}

		_, err := db.Exec(`UPDATE agentic_tasks SET name=?, prompt=?, schedule=?, enabled=?, working_dir=? WHERE id=?`,
			body.Name, body.Prompt, body.Schedule, enabled, body.WorkingDir, body.ID)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				http.Error(w, `{"error":"task name already exists"}`, http.StatusConflict)
				return
			}
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Remove old schedule and re-add if enabled
		s.RemoveTask(agenticTaskName(oldName))
		if body.Enabled {
			scheduleAgenticTask(s, db, bus, body.ID, body.Name, body.Prompt, body.Schedule, body.WorkingDir)
		}

		bus.Publish(Event{Type: "agentic:update", Data: map[string]any{"action": "updated", "id": body.ID}})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleDeleteAgenticTask(db *sql.DB, bus *EventBus, s *scheduler.Scheduler) http.HandlerFunc {
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

		var name string
		if err := db.QueryRow(`SELECT name FROM agentic_tasks WHERE id = ?`, body.ID).Scan(&name); err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		db.Exec(`DELETE FROM agentic_tasks WHERE id = ?`, body.ID)
		s.RemoveTask(agenticTaskName(name))

		bus.Publish(Event{Type: "agentic:update", Data: map[string]any{"action": "deleted", "id": body.ID}})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleRunAgenticTask(db *sql.DB, bus *EventBus) http.HandlerFunc {
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

		var name, prompt, workDir string
		err := db.QueryRow(`SELECT name, prompt, working_dir FROM agentic_tasks WHERE id = ?`, body.ID).
			Scan(&name, &prompt, &workDir)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		go runAgenticTask(db, bus, body.ID, name, prompt, workDir)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "running"})
	}
}

// --- Helpers ---

// agenticTaskName returns the scheduler-internal name for an agentic task,
// prefixed to avoid collisions with system task names.
func agenticTaskName(name string) string {
	return "agentic:" + name
}

// runAgenticTask executes a headless agent call and persists the result.
func runAgenticTask(db *sql.DB, bus *EventBus, taskID int, name, prompt, workDir string) {
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	log.Printf("cmdr: agentic task %q (%d) started", name, taskID)

	bus.Publish(Event{Type: "agentic:run", Data: map[string]any{
		"id": taskID, "status": "running",
	}})

	out, err := agt.RunSimple(context.Background(), agent.SimpleConfig{
		Prompt:  prompt,
		WorkDir: workDir,
	})
	now := time.Now().Format(time.RFC3339)

	status := "success"
	if err != nil {
		status = "failed"
		log.Printf("cmdr: agentic task %q (%d) failed: %v", name, taskID, err)
	} else {
		log.Printf("cmdr: agentic task %q (%d) completed: %s", name, taskID, truncate(out, 200))
	}

	db.Exec(`UPDATE agentic_tasks SET last_run_at=?, last_result=?, last_status=? WHERE id=?`,
		now, out, status, taskID)

	bus.Publish(Event{Type: "agentic:run", Data: map[string]any{
		"id": taskID, "status": status, "last_run_at": now,
	}})
}

// scheduleAgenticTask registers an agentic task with the scheduler.
func scheduleAgenticTask(s *scheduler.Scheduler, db *sql.DB, bus *EventBus, id int, name, prompt, schedule, workDir string) {
	taskID := id
	taskName := name
	taskPrompt := prompt
	taskWorkDir := workDir

	s.AddTask(scheduler.Task{
		Name:        agenticTaskName(taskName),
		Description: "User-defined agentic task",
		Schedule:    schedule,
		Fn: func() error {
			runAgenticTask(db, bus, taskID, taskName, taskPrompt, taskWorkDir)
			return nil
		},
	})
}

// LoadAgenticTasks loads all enabled agentic tasks from the DB into the scheduler.
// Called once at daemon startup.
func LoadAgenticTasks(s *scheduler.Scheduler, db *sql.DB, bus *EventBus) {
	rows, err := db.Query(`SELECT id, name, prompt, schedule, working_dir FROM agentic_tasks WHERE enabled = 1`)
	if err != nil {
		log.Printf("cmdr: failed to load agentic tasks: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id int
		var name, prompt, schedule, workDir string
		if err := rows.Scan(&id, &name, &prompt, &schedule, &workDir); err != nil {
			log.Printf("cmdr: skipping agentic task: %v", err)
			continue
		}
		scheduleAgenticTask(s, db, bus, id, name, prompt, schedule, workDir)
		count++
	}

	if count > 0 {
		log.Printf("cmdr: loaded %d agentic tasks", count)
	}
}
