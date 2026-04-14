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
	"github.com/mikehu/cmdr/internal/prompts"
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

	tmuxSessions, tmuxErr := tmux.ListSessions()
	if tmuxErr != nil {
		log.Printf("cmdr: poller: tmux list error: %v", tmuxErr)
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
		// Only check task lifecycle if tmux listing succeeded —
		// an empty list would falsely mark all running tasks as completed
		if tmuxErr == nil {
			checkRunningTasks(db, bus, tmuxSessions)
		}
		checkResolvedTasks(db, bus)
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

	ppidMap := getParentPIDs()

	// Collect panes that have a claude process as a direct or indirect child
	claudePIDs := make(map[int]bool, len(sessions))
	for _, s := range sessions {
		claudePIDs[s.PID] = true
	}
	claudePanes := collectClaudePanes(tmuxSessions, claudePIDs, ppidMap)

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

// collectClaudePanes returns panes that are running claude, either directly
// (pane command is "claude") or indirectly (e.g. bash -c '... | claude ...').
func collectClaudePanes(sessions []tmux.Session, claudePIDs map[int]bool, ppidMap map[int]int) []claudePane {
	// For each pane, check if any known claude PID is a descendant
	paneAncestor := func(panePID int) bool {
		for cPID := range claudePIDs {
			visited := make(map[int]bool)
			for cur := cPID; cur > 1 && !visited[cur]; cur = ppidMap[cur] {
				visited[cur] = true
				if cur == panePID {
					return true
				}
			}
		}
		return false
	}

	var panes []claudePane
	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.Command == "claude" || paneAncestor(p.PID) {
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

// --- Unified task lifecycle polling ---
//
// All task types (review→refactor, directive) follow the same state machine:
//   running/refactoring → scrape pane for PR → resolved; or window gone → completed
//   resolved → PR closed + worktree gone → completed
//
// The only differences are the window name and worktree name conventions,
// which are derived from task type and ID via taskWindowName/taskWorktreeInfo.

// taskWindowName returns the tmux window name for a task based on its type.
func taskWindowName(taskType, status string, taskID int) string {
	if taskType == "delegation" {
		return fmt.Sprintf("enlist-%d", taskID)
	}
	if taskType == "directive" && status == "implementing" {
		return fmt.Sprintf("impl-%d", taskID)
	}
	if taskType == "directive" {
		return fmt.Sprintf("task-%d", taskID)
	}
	return fmt.Sprintf("review-%d", taskID) // review-triggered refactors
}

// checkRunningTasks monitors all active tasks (running/refactoring/implementing).
// For PR-producing tasks: scrapes tmux pane for PR URL → resolved.
// When the tmux window is gone: marks completed.
func checkRunningTasks(db *sql.DB, bus *EventBus, tmuxSessions []tmux.Session) {
	// Exclude headless tasks (review, ask) — they run via claude -p, not tmux windows,
	// so window liveness checks would falsely mark them as completed.
	rows, err := db.Query(`
		SELECT id, type, repo_path, COALESCE(intent, ''), status, COALESCE(started_at, created_at)
		FROM claude_tasks
		WHERE status IN ('running', 'refactoring', 'implementing')
		  AND NOT (type IN ('review', 'ask') AND status = 'running')
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Build window lookup: windowName → "session:window" target for pane scraping
	allWindows := make(map[string]bool)
	windowTargets := make(map[string]string)
	for _, s := range tmuxSessions {
		for _, w := range s.Windows {
			allWindows[w.Name] = true
			windowTargets[w.Name] = fmt.Sprintf("%s:%s", s.Name, w.Name)
		}
	}

	type task struct {
		id        int
		taskType  string
		repoPath  string
		intent    string
		status    string
		startedAt string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.taskType, &t.repoPath, &t.intent, &t.status, &t.startedAt); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		windowName := taskWindowName(t.taskType, t.status, t.id)
		windowAlive := allWindows[windowName]
		inDesignPhase := t.status == "running" && prompts.IntentHasDesignPhase(t.intent)

		// Design-phase tasks: capture ADR from worktree as soon as it appears.
		// The ADR is the deliverable — once captured, the task is complete
		// regardless of whether the window is still open.
		if inDesignPhase {
			var existingResult string
			db.QueryRow(`SELECT COALESCE(result, '') FROM claude_tasks WHERE id=?`, t.id).Scan(&existingResult)
			if existingResult == "" {
				worktreeName := buildWorktreeName("directive", t.id)
				if adr := scrapeADRFromWorktree(t.repoPath, worktreeName, t.startedAt); adr != "" {
					now := time.Now().Format(time.RFC3339)
					title := extractTitle(adr)
					db.Exec(`UPDATE claude_tasks SET status='completed', result=?, title=?, completed_at=? WHERE id=?`,
						adr, title, now, t.id)
					bus.Publish(Event{Type: "claude:task", Data: map[string]any{
						"id": t.id, "status": "completed", "title": title,
					}})
					enhanceTitle(db, bus, t.id, truncate(adr, 1000))
					log.Printf("cmdr: task %d completed (ADR captured)", t.id)
					continue
				}
			} else {
				// ADR already captured — skip further processing
				continue
			}
		}

		// Delegation tasks: capture debrief file as completion signal.
		// Similar to ADR capture — the debrief is the deliverable.
		if t.taskType == "delegation" {
			var existingResult string
			db.QueryRow(`SELECT COALESCE(result, '') FROM claude_tasks WHERE id=?`, t.id).Scan(&existingResult)
			if existingResult != "" {
				continue // debrief already captured
			}
			if debriefPath, debrief := scrapeDebrief(t.id); debrief != "" {
				now := time.Now().Format(time.RFC3339)
				db.Exec(`UPDATE claude_tasks SET status='completed', result=?, completed_at=? WHERE id=?`,
					debrief, now, t.id)
				os.Remove(debriefPath) // clean up after capture
				bus.Publish(Event{Type: "claude:task", Data: map[string]any{
					"id": t.id, "status": "completed",
				}})
				var squadName string
				db.QueryRow(`SELECT squad FROM delegations WHERE task_id = ?`, t.id).Scan(&squadName)
				if squadName != "" {
					bus.Publish(Event{Type: "delegation:update", Data: map[string]any{
						"squad": squadName, "taskId": t.id, "status": "completed",
					}})
				}
				log.Printf("cmdr: task %d completed (debrief captured)", t.id)
				continue
			}
		}

		// Scrape for PR URL if this task is expected to produce one:
		// - refactoring/implementing statuses always produce PRs
		// - running tasks check intent-level flag, but NOT during design phase
		//   (design phase produces an ADR, not a PR — scraping would pick up
		//   incidental PR URLs from the conversation)
		shouldScrape := t.status == "refactoring" || t.status == "implementing" || (prompts.IntentProducesPR(t.intent) && !inDesignPhase)
		if shouldScrape {
			if target, ok := windowTargets[windowName]; ok {
				if prUrl := scrapePaneForPR(target); prUrl != "" {
					now := time.Now().Format(time.RFC3339)
					db.Exec(`UPDATE claude_tasks SET status='resolved', pr_url=?, completed_at=? WHERE id=?`, prUrl, now, t.id)
					bus.Publish(Event{Type: "claude:task", Data: map[string]any{
						"id": t.id, "status": "resolved", "prUrl": prUrl,
					}})
					log.Printf("cmdr: task %d resolved via pane scrape (PR: %s)", t.id, prUrl)
					continue
				}
			}
		}

		// Window gone — task completed (or failed for design tasks with no ADR)
		if !windowAlive {
			now := time.Now().Format(time.RFC3339)

			if inDesignPhase {
				// If we reach here, the ADR was never captured
				errMsg := "design session closed without producing an ADR"
				db.Exec(`UPDATE claude_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`, errMsg, now, t.id)
				bus.Publish(Event{Type: "claude:task", Data: map[string]any{
					"id": t.id, "status": "failed", "errorMsg": errMsg,
				}})
				log.Printf("cmdr: task %d failed (no ADR found)", t.id)
				continue
			}

			db.Exec(`UPDATE claude_tasks SET status='completed', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "completed",
			}})
			// Publish delegation-specific event for squad summary refresh
			if t.taskType == "delegation" {
				var squadName string
				db.QueryRow(`SELECT squad FROM delegations WHERE task_id = ?`, t.id).Scan(&squadName)
				if squadName != "" {
					bus.Publish(Event{Type: "delegation:update", Data: map[string]any{
						"squad": squadName, "taskId": t.id, "status": "completed",
					}})
				}
			}
			log.Printf("cmdr: task %d completed (window closed)", t.id)
		}
	}
}

// checkResolvedTasks monitors all tasks in "resolved" status.
// When the PR is merged/closed AND the worktree is gone, marks completed.
func checkResolvedTasks(db *sql.DB, bus *EventBus) {
	rows, err := db.Query(`SELECT id, type, repo_path, COALESCE(pr_url, '') FROM claude_tasks WHERE status = 'resolved'`)
	if err != nil {
		return
	}
	defer rows.Close()

	type task struct {
		id       int
		taskType string
		repoPath string
		prUrl    string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.taskType, &t.repoPath, &t.prUrl); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		worktreeName, _ := taskWorktreeInfo(t.taskType, "resolved", t.id)
		worktreeExists := worktreeAlive(t.repoPath, worktreeName)
		prOpen := t.prUrl != "" && isPROpen(t.repoPath, t.prUrl)

		if !prOpen && !worktreeExists {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE claude_tasks SET status='done', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "claude:task", Data: map[string]any{
				"id": t.id, "status": "done",
			}})
			log.Printf("cmdr: task %d done (PR merged/closed, worktree gone)", t.id)
		}
	}
}

// --- PR + worktree helpers ---

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
	re := regexp.MustCompile(`/pull/(\d+)$`)
	matches := re.FindStringSubmatch(prUrl)
	if len(matches) < 2 {
		return false
	}
	cmd := exec.Command("gh", "pr", "view", matches[1], "--json", "state", "-q", ".state")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return true // assume open on error to avoid false "done" transitions
	}
	return strings.TrimSpace(string(out)) == "OPEN"
}

// scrapePaneForPR captures a tmux pane's content and looks for a GitHub PR URL.
func scrapePaneForPR(target string) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", target, "-p", "-S", "-100").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)
	if match := re.Find(out); match != nil {
		return string(match)
	}
	return ""
}

// scrapeADRFromWorktree finds an ADR-*.md file in the worktree's docs/ directory
// that was modified after the task started. Ignores inherited ADRs from before the task.
func scrapeADRFromWorktree(repoPath, worktreeName, startedAt string) string {
	docsDir := filepath.Join(repoPath, ".claude", "worktrees", worktreeName, "docs")
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return ""
	}

	taskStart, _ := time.Parse(time.RFC3339, startedAt)
	// Require ADR to be written meaningfully after task start — worktree checkout
	// touches all files at creation time, so a buffer avoids false positives.
	threshold := taskStart.Add(60 * time.Second)

	var latestName string
	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(strings.ToUpper(e.Name()), "ADR-") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Only consider ADRs modified well after the task started
		if !taskStart.IsZero() && !info.ModTime().After(threshold) {
			continue
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latestName = e.Name()
		}
	}
	if latestName == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(docsDir, latestName))
	if err != nil {
		return ""
	}
	return string(data)
}

// scrapeDebrief checks if a delegation debrief file exists at /tmp/cmdr/debrief-{taskID}.md.
// Returns the file path and contents, or empty strings if not found.
func scrapeDebrief(taskID int) (string, string) {
	path := filepath.Join(os.TempDir(), "cmdr", fmt.Sprintf("debrief-%d.md", taskID))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return path, string(data)
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
