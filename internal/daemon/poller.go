package daemon

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	// Check for completed tasks every 60s (12 ticks)
	if tickCount%12 == 0 {
		checkRefactoringTasks(db, bus, tmuxSessions)
		checkRunningDirectives(db, bus, tmuxSessions)
		publishCommitWatermark(bus, db)
	}

	// Refresh brew outdated every 30m (360 ticks) or on first tick
	if tickCount%360 == 0 {
		go refreshBrewOutdated(bus)
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

// checkRefactoringTasks manages the lifecycle of refactoring tasks:
//   - refactoring: scrape tmux pane for PR URL → resolved; or window gone → completed
//   - resolved: check if PR is merged/closed AND worktree is gone → completed
func checkRefactoringTasks(db *sql.DB, bus *EventBus, tmuxSessions []tmux.Session) {
	checkRefactoringInProgress(db, bus, tmuxSessions)
	checkResolvedPRs(db, bus)
}

// checkRefactoringInProgress handles tasks in "refactoring" status.
func checkRefactoringInProgress(db *sql.DB, bus *EventBus, tmuxSessions []tmux.Session) {
	rows, err := db.Query(`SELECT id, repo_path FROM claude_tasks WHERE status = 'refactoring'`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Collect all window names in the claude_refactor session
	refactorWindows := make(map[string]bool)
	for _, s := range tmuxSessions {
		if s.Name != "claude_refactor" {
			continue
		}
		for _, w := range s.Windows {
			refactorWindows[w.Name] = true
		}
	}

	type task struct {
		id       int
		repoPath string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.repoPath); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		windowName := fmt.Sprintf("review-%d", t.id)
		windowAlive := refactorWindows[windowName]
		target := fmt.Sprintf("claude_refactor:%s", windowName)

		// Try to scrape a PR URL from the pane output
		if prUrl := scrapePaneForPR(target); prUrl != "" {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='resolved', pr_url=?, completed_at=? WHERE id=?`, prUrl, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "resolved", "prUrl": prUrl,
			}})
			log.Printf("cmdr: task %d resolved via pane scrape (PR: %s)", t.id, prUrl)
			continue
		}

		// Window gone and no PR found — cancelled/failed
		if !windowAlive {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='completed', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "completed",
			}})
			log.Printf("cmdr: task %d completed (window closed, no PR found)", t.id)
			cleanupRefactorMarker(t.id)
		}
	}
}

// checkResolvedPRs handles tasks in "resolved" status — checks if the PR has
// been merged/closed AND the worktree is gone, then marks completed.
func checkResolvedPRs(db *sql.DB, bus *EventBus) {
	rows, err := db.Query(`SELECT id, repo_path, COALESCE(pr_url, '') FROM claude_tasks WHERE status = 'resolved'`)
	if err != nil {
		return
	}
	defer rows.Close()

	type task struct {
		id       int
		repoPath string
		prUrl    string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.repoPath, &t.prUrl); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		worktreeName := fmt.Sprintf("refactor-review-%d", t.id)
		worktreeExists := worktreeAlive(t.repoPath, worktreeName)
		prOpen := t.prUrl != "" && isPROpen(t.repoPath, t.prUrl)

		// Only complete when PR is no longer open AND worktree is gone
		if !prOpen && !worktreeExists {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='completed', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "completed",
			}})
			log.Printf("cmdr: task %d completed (PR merged/closed, worktree gone)", t.id)
			cleanupRefactorMarker(t.id)
		}
	}
}

// checkRunningDirectives monitors directive tasks in "running" status.
// When the tmux window (task-{id}) is gone, the task is marked completed.
func checkRunningDirectives(db *sql.DB, bus *EventBus, tmuxSessions []tmux.Session) {
	rows, err := db.Query(`SELECT id, repo_path FROM claude_tasks WHERE type='directive' AND status='running'`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Collect all window names across all sessions
	allWindows := make(map[string]bool)
	for _, s := range tmuxSessions {
		for _, w := range s.Windows {
			allWindows[w.Name] = true
		}
	}

	type task struct {
		id       int
		repoPath string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.repoPath); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		windowName := fmt.Sprintf("task-%d", t.id)
		if !allWindows[windowName] {
			// Window gone — directive completed
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='completed', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "completed",
			}})
			log.Printf("cmdr: directive task %d completed (window closed)", t.id)
		}
	}
}

// worktreeAlive checks if a named worktree exists in the repo.
func worktreeAlive(repoPath, worktreeName string) bool {
	out, err := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	target := filepath.Join(".claude", "worktrees", worktreeName)
	return strings.Contains(string(out), target)
}

// isPROpen checks if a PR URL is still open (not merged or closed).
func isPROpen(repoPath, prUrl string) bool {
	// Extract PR number from URL
	re := regexp.MustCompile(`/pull/(\d+)$`)
	matches := re.FindStringSubmatch(prUrl)
	if len(matches) < 2 {
		return false
	}
	cmd := exec.Command("gh", "pr", "view", matches[1], "--json", "state", "-q", ".state")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return false // can't check, assume not open
	}
	return strings.TrimSpace(string(out)) == "OPEN"
}

// scrapePaneForPR captures a tmux pane's content and looks for a GitHub PR URL.
func scrapePaneForPR(target string) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", target, "-p", "-S", "-100").Output()
	if err != nil {
		return ""
	}
	// Match GitHub PR URLs like https://github.com/owner/repo/pull/123
	re := regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)
	if match := re.Find(out); match != nil {
		return string(match)
	}
	return ""
}

// cleanupRefactorMarker removes the marker file for a resolved/completed task.
func cleanupRefactorMarker(taskID int) {
	worktreeName := fmt.Sprintf("refactor-review-%d", taskID)
	markerPath := filepath.Join(os.Getenv("HOME"), ".cmdr", "refactors", worktreeName)
	os.Remove(markerPath)
}

// publishCommitWatermark sends the latest commit ID so the frontend can detect staleness.
func publishCommitWatermark(bus *EventBus, db *sql.DB) {
	var latestID int
	db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM commits`).Scan(&latestID)
	bus.Publish(Event{Type: "commits:watermark", Data: map[string]any{"latestId": latestID}})
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
