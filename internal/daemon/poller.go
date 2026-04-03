package daemon

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mikehu/cmdr/internal/claude"
	"github.com/mikehu/cmdr/internal/scheduler"
	"github.com/mikehu/cmdr/internal/tmux"
)

// startPoller runs server-side polling and publishes events to the bus.
func startPoller(bus *EventBus, s *scheduler.Scheduler) func() {
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Publish initial state immediately
		publishStatus(bus, s)
		publishTmux(bus)
		publishClaude(bus)

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				publishStatus(bus, s)
				publishTmux(bus)
				publishClaude(bus)
			}
		}
	}()

	return func() { close(done) }
}

func publishStatus(bus *EventBus, s *scheduler.Scheduler) {
	bus.Publish(Event{
		Type: "status",
		Data: map[string]any{
			"status":  "running",
			"version": Version,
			"pid":     os.Getpid(),
			"tasks":   len(s.Tasks()),
		},
	})
}

func publishTmux(bus *EventBus) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		log.Printf("cmdr: poller: tmux list error: %v", err)
		return
	}
	bus.Publish(Event{
		Type: "tmux:sessions",
		Data: sessions,
	})
}

func publishClaude(bus *EventBus) {
	sessions, err := claude.ListSessions()
	if err != nil {
		log.Printf("cmdr: poller: claude list error: %v", err)
		return
	}

	// Find Claude panes in tmux and check attention state.
	// Match by: Claude session cwd matches tmux pane cwd, and pane runs "claude".
	tmuxSessions, _ := tmux.ListSessions()
	claudePanes := collectClaudePanes(tmuxSessions)

	for i := range sessions {
		for _, cp := range claudePanes {
			if cp.cwd == sessions[i].CWD {
				sessions[i].TmuxTarget = cp.target
				sessions[i].Status = claude.PaneStatus(cp.target)
				break
			}
		}
	}

	bus.Publish(Event{
		Type: "claude:sessions",
		Data: sessions,
	})
}

type claudePane struct {
	target string // e.g. "cmdr:1.3"
	cwd    string
}

func collectClaudePanes(sessions []tmux.Session) []claudePane {
	var panes []claudePane
	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.Command == "claude" {
					target := fmt.Sprintf("%s:%d.%d", s.Name, w.Index, p.Index)
					panes = append(panes, claudePane{target: target, cwd: p.CWD})
				}
			}
		}
	}
	return panes
}
