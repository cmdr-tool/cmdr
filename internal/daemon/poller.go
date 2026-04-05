package daemon

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mikehu/cmdr/internal/claude"
	"github.com/mikehu/cmdr/internal/scheduler"
	"github.com/mikehu/cmdr/internal/tmux"
)

const (
	sleepThreshold = 15 * time.Second // gap > this means system was sleeping
	awayIdleTime   = 5 * time.Minute  // HIDIdleTime > this means user is away
)

// startPoller runs server-side polling and publishes events to the bus.
func startPoller(bus *EventBus, s *scheduler.Scheduler, db *sql.DB) func() {
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var tickCount int
		lastTick := time.Now()
		pollTick(bus, s, db, false, tickCount)

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				tickCount++
				now := time.Now()
				gap := now.Sub(lastTick)
				lastTick = now

				if gap > sleepThreshold {
					log.Printf("cmdr: poller: wake detected (gap %v), backfilling sleep buckets", gap.Round(time.Second))
					backfillSleep(db, now.Add(-gap), now)
				}

				away := systemIdleTime() > awayIdleTime
				pollTick(bus, s, db, away, tickCount)
			}
		}
	}()

	return func() { close(done) }
}

// pollTick runs a single polling cycle.
// tickCount drives sub-frequencies: analytics publishes every 12 ticks (60s).
func pollTick(bus *EventBus, s *scheduler.Scheduler, db *sql.DB, away bool, tickCount int) {
	publishStatus(bus, s)

	tmuxSessions, err := tmux.ListSessions()
	if err != nil {
		log.Printf("cmdr: poller: tmux list error: %v", err)
		tmuxSessions = []tmux.Session{}
	}

	claudeSessions := enrichAndPublishClaude(bus, tmuxSessions)

	if !away {
		bus.Publish(Event{Type: "tmux:sessions", Data: tmuxSessions})
	}

	now := time.Now()
	recordActivity(db, tmuxSessions, claudeSessions, now, away)

	// Publish analytics snapshot every 60s (12 ticks)
	if tickCount%12 == 0 {
		publishAnalytics(bus, db, now)
	}
}

// systemIdleTime returns how long since the last keyboard/mouse input.
func systemIdleTime() time.Duration {
	out, err := exec.Command("/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0
	}
	// Parse HIDIdleTime from ioreg output (value is in nanoseconds)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "HIDIdleTime") && !strings.Contains(line, "HIDIdleTimeDelta") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				if ns, err := strconv.ParseInt(val, 10, 64); err == nil {
					return time.Duration(ns)
				}
			}
		}
	}
	return 0
}

// publishAnalytics queries today+yesterday at 5m resolution and publishes via SSE.
func publishAnalytics(bus *EventBus, db *sql.DB, now time.Time) {
	todaySlot := now.YearDay() % 2
	yesterdaySlot := (todaySlot + 1) % 2
	mergeCount := 60 // 5m

	_, curBucket := currentBucket(now)

	bus.Publish(Event{
		Type: "analytics:activity",
		Data: activityResponse{
			Resolution:    "5m",
			SamplesPerBar: mergeCount,
			Today: activityDay{
				Date:          now.Format("2006-01-02"),
				CurrentBucket: curBucket / mergeCount,
				Buckets:       querySlot(db, todaySlot, mergeCount),
			},
			Yesterday: activityDay{
				Date:    now.AddDate(0, 0, -1).Format("2006-01-02"),
				Buckets: querySlot(db, yesterdaySlot, mergeCount),
			},
		},
	})
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

// enrichAndPublishClaude matches Claude sessions to tmux panes and publishes them.
// Returns the enriched sessions for use by analytics.
func enrichAndPublishClaude(bus *EventBus, tmuxSessions []tmux.Session) []claude.Session {
	sessions, err := claude.ListSessions()
	if err != nil {
		log.Printf("cmdr: poller: claude list error: %v", err)
		return nil
	}

	claudePanes := collectClaudePanes(tmuxSessions)
	ppidMap := getParentPIDs()

	shellPIDs := make(map[int]*claudePane)
	for i := range claudePanes {
		shellPIDs[claudePanes[i].shellPID] = &claudePanes[i]
	}

	for i := range sessions {
		if cp := findAncestorPane(sessions[i].PID, ppidMap, shellPIDs); cp != nil {
			sessions[i].TmuxTarget = cp.target
			sessions[i].Status = claude.PaneStatus(cp.target)
		}
	}

	bus.Publish(Event{Type: "claude:sessions", Data: sessions})
	return sessions
}

type claudePane struct {
	target   string // e.g. "cmdr:1.3"
	shellPID int    // PID of the shell process in the pane
}

func collectClaudePanes(sessions []tmux.Session) []claudePane {
	var panes []claudePane
	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.Command == "claude" {
					target := fmt.Sprintf("%s:%d.%d", s.Name, w.Index, p.Index)
					panes = append(panes, claudePane{target: target, shellPID: p.PID})
				}
			}
		}
	}
	return panes
}

// findAncestorPane walks up the process tree from pid to find a matching pane shell.
// Handles intermediate processes (e.g., zsh → volta-shim → node).
func findAncestorPane(pid int, ppidMap map[int]int, shellPIDs map[int]*claudePane) *claudePane {
	visited := make(map[int]bool)
	for cur := pid; cur > 1 && !visited[cur]; cur = ppidMap[cur] {
		visited[cur] = true
		if cp, ok := shellPIDs[cur]; ok {
			return cp
		}
	}
	return nil
}

// getParentPIDs returns a map of PID → PPID for all processes.
// Single `ps` call, efficient for matching Claude PIDs to pane shell PIDs.
func getParentPIDs() map[int]int {
	out, err := exec.Command("ps", "-eo", "pid,ppid").Output()
	if err != nil {
		return nil
	}
	m := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil {
			m[pid] = ppid
		}
	}
	return m
}
