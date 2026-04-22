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

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/proc"
	"github.com/cmdr-tool/cmdr/internal/prompts"
	"github.com/cmdr-tool/cmdr/internal/scheduler"
	"github.com/cmdr-tool/cmdr/internal/terminal"
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

	termSessions, termErr := term.ListSessions()
	if termErr != nil {
		log.Printf("cmdr: poller: tmux list error: %v", termErr)
		termSessions = []terminal.Session{}
	}

	agentInstances := enrichAndPublishAgents(bus, termSessions)

	if !away {
		bus.Publish(Event{Type: "tmux:sessions", Data: termSessions})
	}

	now := time.Now()
	recordActivity(db, termSessions, agentInstances, now, away)

	// Publish analytics snapshot every 60s (12 ticks)
	if tickCount%12 == 0 {
		publishAnalytics(bus, db, now)
	}

	// Check for completed tasks every 30s (6 ticks)
	if tickCount%6 == 0 {
		// Only check task lifecycle if tmux listing succeeded —
		// an empty list would falsely mark all running tasks as completed
		if termErr == nil {
			checkRunningTasks(db, bus, termSessions)
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

// collectAgentInstances detects all running agent instances across registered
// adapters, matches them to panes, scrapes status, and mutates termSessions to
// replace shim commands (e.g. volta-shim → pi).
func collectAgentInstances(termSessions []terminal.Session) []agent.Instance {
	snapshot, err := proc.List()
	if err != nil {
		log.Printf("cmdr: poller: process snapshot error: %v", err)
	}
	ppidMap := parentPIDMap(snapshot)

	var allInstances []agent.Instance
	paneOverrides := make(map[string]string) // tmuxTarget → agent name

	allAgents := agent.All()
	for _, a := range allAgents {
		instances, err := a.DetectInstances(snapshot)
		if err != nil {
			log.Printf("cmdr: poller: %s detect error: %v", a.Name(), err)
			continue
		}
		if len(instances) == 0 {
			continue
		}

		agentPIDs := make(map[int]bool, len(instances))
		for _, inst := range instances {
			agentPIDs[inst.PID] = true
		}

		panes := collectAgentPanes(termSessions, a.ProcessName(), agentPIDs, ppidMap)
		shellPIDs := make(map[int]*agentPane)
		for i := range panes {
			shellPIDs[panes[i].shellPID] = &panes[i]
		}

		for i := range instances {
			if cp := findAncestorPane(instances[i].PID, ppidMap, shellPIDs); cp != nil {
				instances[i].TmuxTarget = cp.target
				if instances[i].CWD == "" {
					instances[i].CWD = cp.cwd
					instances[i].Project = filepath.Base(cp.cwd)
				}
				paneOverrides[cp.target] = a.Name()
				instances[i].Status = a.PaneStatus(capturePaneLines(cp.target))
			}
		}

		allInstances = append(allInstances, instances...)
	}

	if allInstances == nil {
		allInstances = []agent.Instance{}
	}
	overridePaneCommands(termSessions, paneOverrides)
	return allInstances
}

// enrichAndPublishAgents detects all running agent instances across registered
// adapters, matches them to tmux panes, scrapes status, and publishes via SSE.
func enrichAndPublishAgents(bus *EventBus, termSessions []terminal.Session) []agent.Instance {
	allInstances := collectAgentInstances(termSessions)
	bus.Publish(Event{Type: "agent:sessions", Data: allInstances})
	return allInstances
}

type agentPane struct {
	target   string // e.g. "cmdr:1.3"
	shellPID int    // PID of the shell process in the pane
	cwd      string // pane's working directory
}

// collectAgentPanes returns panes running a specific agent, matched by
// command name or PID ancestry.
func collectAgentPanes(sessions []terminal.Session, processName string, agentPIDs map[int]bool, ppidMap map[int]int) []agentPane {
	paneAncestor := func(panePID int) bool {
		for aPID := range agentPIDs {
			visited := make(map[int]bool)
			for cur := aPID; cur > 1 && !visited[cur]; cur = ppidMap[cur] {
				visited[cur] = true
				if cur == panePID {
					return true
				}
			}
		}
		return false
	}

	var panes []agentPane
	forEachPane(sessions, func(target string, p *terminal.Pane) {
		if p.Command == processName || paneAncestor(p.PID) {
			panes = append(panes, agentPane{target: target, shellPID: p.PID, cwd: p.CWD})
		}
	})
	return panes
}

// capturePaneLines captures terminal output from a tmux pane and returns it as lines.
func capturePaneLines(target string) []string {
	out, err := term.CapturePane(target, 100)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

// findAncestorPane walks up the process tree from pid to find a matching pane shell.
func findAncestorPane(pid int, ppidMap map[int]int, shellPIDs map[int]*agentPane) *agentPane {
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
// Every task follows a simple lifecycle: running → completed | failed.
// The poller monitors running tasks for artifact completion (ADR, PR, debrief)
// and window liveness. Headless tasks (ask, review, analysis) are managed by
// their goroutines, not the poller.

// taskWindowName returns the tmux window name for a task based on its type/intent.
func taskWindowName(taskType, intent string, taskID int) string {
	if taskType == "delegation" {
		return fmt.Sprintf("enlist-%d", taskID)
	}
	prefix := "task"
	if intent != "" {
		prefix = intent
	}
	return fmt.Sprintf("%s-%d", prefix, taskID)
}

// checkRunningTasks monitors all interactive running tasks.
// Detects artifact completion (ADR, PR, debrief) and window closure.
func checkRunningTasks(db *sql.DB, bus *EventBus, termSessions []terminal.Session) {
	rows, err := db.Query(`
		SELECT id, type, repo_path, COALESCE(intent, ''), worktree,
		       COALESCE(started_at, created_at), COALESCE(terminal_target, '')
		FROM agent_tasks
		WHERE status = 'running'
		  AND NOT (type IN ('review', 'ask'))
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	type task struct {
		id             int
		taskType       string
		repoPath       string
		intent         string
		worktree       string
		startedAt      string
		terminalTarget string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.taskType, &t.repoPath, &t.intent, &t.worktree, &t.startedAt, &t.terminalTarget); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		meta := prompts.GetIntentMeta(t.intent)

		// Skip headless intents — managed by runHeadless goroutine
		if meta.Mode == "headless" {
			continue
		}

		// Check if the task's terminal window is still alive.
		// Prefer stored terminal_target (adapter-native ref); fall back to session scan.
		var windowAlive bool
		target := t.terminalTarget
		if target != "" {
			windowAlive = term.WindowExists(target)
		} else {
			windowName := taskWindowName(t.taskType, t.intent, t.id)
			target, windowAlive = terminal.FindWindowTarget(termSessions, windowName)
		}

		// ADR-producing tasks: capture ADR from worktree as completion signal
		if meta.Artifact == "adr" {
			var existingResult string
			db.QueryRow(`SELECT COALESCE(result, '') FROM agent_tasks WHERE id=?`, t.id).Scan(&existingResult)
			if existingResult == "" {
				if adr := scrapeADRFromWorktree(t.repoPath, t.worktree, t.startedAt); adr != "" {
					now := time.Now().Format(time.RFC3339)
					title := extractTitle(adr)
					db.Exec(`UPDATE agent_tasks SET status='resolved', result=?, title=?, completed_at=? WHERE id=?`,
						adr, title, now, t.id)
					bus.Publish(Event{Type: "agent:task", Data: map[string]any{
						"id": t.id, "status": "resolved", "title": title,
					}})
					enhanceTitle(db, bus, t.id, truncate(adr, 1000))
					log.Printf("cmdr: task %d resolved (ADR captured, awaiting review)", t.id)
					continue
				}
			} else {
				continue // ADR already captured
			}

			// Window gone without ADR → failed
			if !windowAlive {
				now := time.Now().Format(time.RFC3339)
				errMsg := "design session closed without producing an ADR"
				db.Exec(`UPDATE agent_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`, errMsg, now, t.id)
				bus.Publish(Event{Type: "agent:task", Data: map[string]any{
					"id": t.id, "status": "failed", "errorMsg": errMsg,
				}})
				log.Printf("cmdr: task %d failed (no ADR found)", t.id)
			}
			continue
		}

		// Delegation tasks: capture debrief file as completion signal
		if t.taskType == "delegation" {
			var existingResult string
			db.QueryRow(`SELECT COALESCE(result, '') FROM agent_tasks WHERE id=?`, t.id).Scan(&existingResult)
			if existingResult != "" {
				continue
			}
			if debriefPath, debrief := scrapeDebrief(t.id); debrief != "" {
				now := time.Now().Format(time.RFC3339)
				db.Exec(`UPDATE agent_tasks SET status='completed', result=?, completed_at=? WHERE id=?`,
					debrief, now, t.id)
				os.Remove(debriefPath)
				bus.Publish(Event{Type: "agent:task", Data: map[string]any{
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

		// PR-producing tasks: scrape pane for PR URL → resolved (awaiting merge)
		if meta.Artifact == "pr" && windowAlive && target != "" {
			if prUrl := scrapePaneForPR(target); prUrl != "" {
				now := time.Now().Format(time.RFC3339)
				db.Exec(`UPDATE agent_tasks SET status='resolved', pr_url=?, completed_at=? WHERE id=?`, prUrl, now, t.id)
				bus.Publish(Event{Type: "agent:task", Data: map[string]any{
					"id": t.id, "status": "resolved", "prUrl": prUrl,
				}})
				log.Printf("cmdr: task %d resolved (PR: %s)", t.id, prUrl)
				continue
			}
		}

		// Window gone → resolve or complete based on task type
		if !windowAlive {
			now := time.Now().Format(time.RFC3339)
			// Reviews produce an artifact (findings) that needs user action
			// before the lifecycle is complete — land in "resolved" not "completed"
			status := "completed"
			if t.taskType == "review" {
				status = "resolved"
			}
			db.Exec(`UPDATE agent_tasks SET status=?, completed_at=? WHERE id=?`, status, now, t.id)
			bus.Publish(Event{Type: "agent:task", Data: map[string]any{
				"id": t.id, "status": status,
			}})
			if t.taskType == "delegation" {
				var squadName string
				db.QueryRow(`SELECT squad FROM delegations WHERE task_id = ?`, t.id).Scan(&squadName)
				if squadName != "" {
					bus.Publish(Event{Type: "delegation:update", Data: map[string]any{
						"squad": squadName, "taskId": t.id, "status": "completed",
					}})
				}
			}
			log.Printf("cmdr: task %d %s (window closed)", t.id, status)
		}
	}
}

// checkResolvedTasks monitors tasks with PRs awaiting merge.
// When the PR is merged/closed AND the worktree is gone, marks completed.
func checkResolvedTasks(db *sql.DB, bus *EventBus) {
	rows, err := db.Query(`SELECT id, repo_path, worktree, COALESCE(pr_url, '') FROM agent_tasks WHERE status = 'resolved' AND pr_url != ''`)
	if err != nil {
		return
	}
	defer rows.Close()

	type task struct {
		id       int
		repoPath string
		worktree string
		prUrl    string
	}
	var tasks []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.repoPath, &t.worktree, &t.prUrl); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	for _, t := range tasks {
		worktreeExists := t.worktree != "" && worktreeAlive(t.repoPath, t.worktree)
		prOpen := t.prUrl != "" && isPROpen(t.repoPath, t.prUrl)

		if !prOpen && !worktreeExists {
			now := time.Now().Format(time.RFC3339)
			db.Exec(`UPDATE agent_tasks SET status='completed', completed_at=? WHERE id=?`, now, t.id)
			bus.Publish(Event{Type: "agent:task", Data: map[string]any{
				"id": t.id, "status": "completed",
			}})
			log.Printf("cmdr: task %d completed (PR merged, worktree gone)", t.id)
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
	s := string(out)
	// Check both raw name and sanitized (/ → +) form
	if strings.Contains(s, filepath.Join(".claude", "worktrees", worktreeName)) {
		return true
	}
	sanitized := strings.ReplaceAll(worktreeName, "/", "+")
	return sanitized != worktreeName && strings.Contains(s, filepath.Join(".claude", "worktrees", sanitized))
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
		return true // assume open on error to avoid false completions
	}
	return strings.TrimSpace(string(out)) == "OPEN"
}

// --- Pane scraping helpers ---

// scrapePaneForPR captures a pane's content and looks for a GitHub PR URL.
func scrapePaneForPR(target string) string {
	content, err := term.CapturePane(target, 100)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)
	if match := re.FindString(content); match != "" {
		return match
	}
	return ""
}

// scrapeADRFromWorktree finds an ADR-*.md file in the worktree's docs/ directory
// that was modified after the task started. Ignores inherited ADRs from before the task.
func scrapeADRFromWorktree(repoPath, worktreeName, startedAt string) string {
	docsDir := filepath.Join(worktreeDir(repoPath, worktreeName), "docs")
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

func overridePaneCommands(sessions []terminal.Session, overrides map[string]string) {
	forEachPane(sessions, func(target string, pane *terminal.Pane) {
		if agentName, ok := overrides[target]; ok {
			pane.Command = agentName
		}
	})
}

func forEachPane(sessions []terminal.Session, fn func(target string, pane *terminal.Pane)) {
	for si := range sessions {
		for wi := range sessions[si].Windows {
			for pi := range sessions[si].Windows[wi].Panes {
				pane := &sessions[si].Windows[wi].Panes[pi]
				target := fmt.Sprintf("%s:%d.%d", sessions[si].Name, sessions[si].Windows[wi].Index, pane.Index)
				fn(target, pane)
			}
		}
	}
}

func parentPIDMap(snapshot *proc.Snapshot) map[int]int {
	if snapshot == nil {
		return nil
	}
	return snapshot.ParentMap()
}
