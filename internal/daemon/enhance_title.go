package daemon

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/mikehu/cmdr/internal/ollama"
)

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
