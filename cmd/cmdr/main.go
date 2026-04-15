package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	cmdr "github.com/mikehu/cmdr"
	"github.com/mikehu/cmdr/internal/daemon"
	"github.com/mikehu/cmdr/internal/db"
	"github.com/mikehu/cmdr/internal/prompts"
	"github.com/mikehu/cmdr/internal/scheduler"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	// Set version and embedded SPA filesystem for the daemon
	daemon.Version = version
	if webFS, err := fs.Sub(cmdr.WebBuildFS, "web/build"); err == nil {
		daemon.WebFS = webFS
	}

	root := &cobra.Command{
		Use:     "cmdr",
		Short:   "Personal command runner and automation daemon",
		Version: version,
	}

	root.AddCommand(startCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(runCmd())
	root.AddCommand(listCmd())
	root.AddCommand(contextCmd())
	root.AddCommand(initCmd())
	root.AddCommand(enlistCmd())
	root.AddCommand(missionsCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func startCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the cmdr daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if foreground {
				return daemon.Run()
			}
			return daemon.Start()
		},
	}
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "Run in foreground (used by launchd)")
	return cmd
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the cmdr daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Stop()
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Status()
		},
	}
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run [task]",
		Short: "Run a task immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()
			s := scheduler.New(database, scheduler.Hooks{})
			return s.RunTask(args[0])
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()
			s := scheduler.New(database, scheduler.Hooks{})
			for _, t := range s.Tasks() {
				fmt.Printf("  %-20s %s\t%s\n", t.Name, t.Schedule, t.Description)
			}
			return nil
		},
	}
}

func contextCmd() *cobra.Command {
	var repoPath string
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Output squad context JSON for Claude Code SessionStart hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoPath == "" {
				var err error
				repoPath, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			// Resolve symlinks for path matching
			if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
				repoPath = resolved
			}
			repoPath = filepath.Clean(repoPath)

			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()

			return printSquadContext(database, repoPath)
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "Repository path (defaults to cwd)")
	return cmd
}

func printSquadContext(database *sql.DB, repoPath string) error {
	var squadName, alias string
	err := database.QueryRow(
		`SELECT squad, squad_alias FROM repos WHERE path = ?`, repoPath,
	).Scan(&squadName, &alias)

	// Try resolving stored paths if exact match fails
	if err != nil {
		rows, _ := database.Query(`SELECT path, squad, squad_alias FROM repos WHERE squad != ''`)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var p, s, a string
				rows.Scan(&p, &s, &a)
				resolved, resolveErr := filepath.EvalSymlinks(p)
				if resolveErr == nil {
					resolved = filepath.Clean(resolved)
				}
				if resolved == repoPath || filepath.Clean(p) == repoPath {
					squadName, alias = s, a
					break
				}
			}
		}
	}

	if squadName == "" {
		return outputHook("SessionStart", "")
	}

	// Query other squad members
	rows, err := database.Query(
		`SELECT squad_alias, path FROM repos WHERE squad = ? AND path != ? ORDER BY squad_alias`,
		squadName, repoPath,
	)
	if err != nil {
		return outputHook("SessionStart", "")
	}
	defer rows.Close()

	var members []string
	for rows.Next() {
		var mAlias, mPath string
		rows.Scan(&mAlias, &mPath)
		members = append(members, fmt.Sprintf("%s (%s)", mAlias, mPath))
	}

	ctx := fmt.Sprintf("You are in squad '%s' as '%s'.", squadName, alias)
	if len(members) > 0 {
		ctx += fmt.Sprintf(" Squad members: %s.", strings.Join(members, ", "))
	}
	ctx += " Use /enlist to request work from squad members."

	// Append active delegation status
	dRows, err := database.Query(
		`SELECT d.to_alias, COALESCE(ct.title, ''), ct.status
		 FROM claude_tasks ct
		 JOIN delegations d ON d.task_id = ct.id
		 WHERE ct.type = 'delegation' AND d.squad = ? AND d.from_alias = ?
		   AND ct.status IN ('running', 'completed')`,
		squadName, alias,
	)
	if err == nil {
		defer dRows.Close()
		for dRows.Next() {
			var dTo, dTitle, dStatus string
			dRows.Scan(&dTo, &dTitle, &dStatus)
			if dStatus == "running" {
				ctx += fmt.Sprintf(" Active enlistment: %s is working on '%s'.", dTo, dTitle)
			} else {
				ctx += fmt.Sprintf(" Enlistment to %s completed: '%s'.", dTo, dTitle)
			}
		}
	}

	return outputHook("SessionStart", ctx)
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up Claude Code integration (hooks and commands)",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// Install /enlist command
			cmdDir := filepath.Join(home, ".claude", "commands")
			os.MkdirAll(cmdDir, 0o755)

			enlistPath := filepath.Join(cmdDir, "enlist.md")
			if err := installEnlistCommand(enlistPath); err != nil {
				return fmt.Errorf("installing enlist command: %w", err)
			}
			fmt.Printf("cmdr: installed %s\n", enlistPath)

			// Merge SessionStart hook into settings.local.json
			settingsPath := filepath.Join(home, ".claude", "settings.local.json")
			if err := mergeHooks(settingsPath); err != nil {
				return fmt.Errorf("merging hooks: %w", err)
			}
			fmt.Printf("cmdr: configured hooks in %s\n", settingsPath)

			return nil
		},
	}
}

func installEnlistCommand(path string) error {
	bin := cmdrBin()
	content := fmt.Sprintf(`Enlist a squad member to help with cross-repo work.

You are part of a squad — a group of repos managed by cmdr that can collaborate on cross-repo work.

## When to use

When your current task requires changes in another repository that is part of your squad. For example:
- You need a new API endpoint in a sibling service
- You need a shared type exported from a common library
- You need a config change in an infrastructure repo

## How to enlist

Run the cmdr CLI to dispatch work to a squad member:

`+"```bash"+`
%s enlist --squad {squad-name} --from {your-alias} --to {target-alias} \
  --summary "Brief description of what you need" \
  --details "Full specification — be precise about interfaces, types, behavior"
`+"```"+`

Cmdr will validate the squad, create a branch, and launch a Claude session in the target repo.

After dispatching, continue working on parts of your task that don't depend on the enlisted work. You will be automatically notified when the enlistment is complete.
`, bin)
	return os.WriteFile(path, []byte(content), 0o644)
}

func cmdrBin() string {
	if p, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return p
	}
	return "cmdr"
}

func mergeHooks(path string) error {
	bin := cmdrBin()
	cmdrHooks := map[string]string{
		"SessionStart":     fmt.Sprintf(`%s context --repo "${CLAUDE_PROJECT_DIR:-$PWD}"`, bin),
		"UserPromptSubmit": fmt.Sprintf(`%s missions --repo "${CLAUDE_PROJECT_DIR:-$PWD}"`, bin),
	}

	// Read existing settings if present
	var settings map[string]any
	data, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = make(map[string]any)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	changed := false
	for event, hookCmd := range cmdrHooks {
		hookList, _ := hooks[event].([]any)

		// Check if our hook already exists
		found := false
		for _, h := range hookList {
			if hMap, ok := h.(map[string]any); ok {
				if cmd, _ := hMap["command"].(string); cmd == hookCmd {
					found = true
					break
				}
			}
		}
		if !found {
			hookList = append(hookList, map[string]any{
				"type":    "command",
				"command": hookCmd,
			})
			hooks[event] = hookList
			changed = true
		}
	}

	if !changed {
		return nil
	}

	settings["hooks"] = hooks
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func enlistCmd() *cobra.Command {
	var squad, from, to, summary, details string
	cmd := &cobra.Command{
		Use:   "enlist",
		Short: "Enlist a squad member for cross-repo work",
		RunE: func(cmd *cobra.Command, args []string) error {
			if squad == "" || from == "" || to == "" || summary == "" {
				return fmt.Errorf("--squad, --from, --to, and --summary are required")
			}

			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()

			// Validate squad exists
			var squadExists bool
			database.QueryRow(`SELECT COUNT(*) FROM squads WHERE name = ?`, squad).Scan(&squadExists)
			if !squadExists {
				return fmt.Errorf("squad %q does not exist", squad)
			}

			// Resolve target alias to repo path
			var targetPath string
			err = database.QueryRow(`SELECT path FROM repos WHERE squad = ? AND squad_alias = ?`, squad, to).Scan(&targetPath)
			if err != nil {
				return fmt.Errorf("squad member %q not found in squad %q", to, squad)
			}

			// Check no running delegation already targets this repo
			var running int
			database.QueryRow(`SELECT COUNT(*) FROM claude_tasks WHERE type = 'delegation' AND repo_path = ? AND status = 'running'`, targetPath).Scan(&running)
			if running > 0 {
				return fmt.Errorf("%s already has an active delegation — wait for it to complete", to)
			}

			// Create task row
			prompt := fmt.Sprintf("## Enlistment from %s\n\n**Summary:** %s\n\n%s", from, summary, details)
			now := time.Now().Format(time.RFC3339)
			taskResult, err := database.Exec(
				`INSERT INTO claude_tasks (type, status, repo_path, prompt, intent, created_at, started_at)
				 VALUES ('delegation', 'running', ?, ?, 'delegation', ?, ?)`,
				targetPath, prompt, now, now,
			)
			if err != nil {
				return fmt.Errorf("creating task: %w", err)
			}
			taskID64, _ := taskResult.LastInsertId()
			taskID := int(taskID64)

			// Create branch
			branchName := fmt.Sprintf("squad/%s/%d", squad, taskID)

			// Insert delegation details
			database.Exec(
				`INSERT INTO delegations (task_id, squad, from_alias, to_alias, branch, summary, details)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				taskID, squad, from, to, branchName, summary, details,
			)
			if out, err := exec.Command("git", "-C", targetPath, "checkout", "-b", branchName).CombinedOutput(); err != nil {
				// Clean up task on failure
				database.Exec(`DELETE FROM claude_tasks WHERE id = ?`, taskID)
				return fmt.Errorf("creating branch: %s", strings.TrimSpace(string(out)))
			}

			// Debrief path in /tmp — transient, captured by poller then deleted
			debriefDir := filepath.Join(os.TempDir(), "cmdr")
			os.MkdirAll(debriefDir, 0o700)
			debriefPath := filepath.Join(debriefDir, fmt.Sprintf("debrief-%d.md", taskID))
			prompt += fmt.Sprintf("\n\n---\n\nDEBRIEF_PATH: %s", debriefPath)

			// Build claude command with delegation system prompt
			escapedPrompt := strings.ReplaceAll(prompt, "'", "'\\''")
			claudeCmd := fmt.Sprintf("claude --name 'cmdr-task-%d'", taskID)

			if systemPrompt, err := prompts.GetIntentPrompt("delegation"); err == nil {
				escapedSystem := strings.ReplaceAll(systemPrompt, "'", "'\\''")
				claudeCmd = fmt.Sprintf("%s --append-system-prompt '%s' '%s'", claudeCmd, escapedSystem, escapedPrompt)
			} else {
				claudeCmd = fmt.Sprintf("%s '%s'", claudeCmd, escapedPrompt)
			}

			// Find or create tmux session for target repo
			windowName := fmt.Sprintf("enlist-%d", taskID)
			tmuxArgs := []string{"new-window", "-n", windowName, "-c", targetPath, "bash", "-c", claudeCmd}

			// Try to find existing session for this repo
			sessionName := findSessionForRepo(targetPath)
			if sessionName != "" {
				tmuxArgs = append([]string{"new-window", "-t", sessionName, "-n", windowName, "-c", targetPath, "bash", "-c", claudeCmd}, []string{}...)
				tmuxArgs = []string{"new-window", "-t", sessionName, "-n", windowName, "-c", targetPath, "bash", "-c", claudeCmd}
			} else {
				// Create a new session
				out, err := exec.Command("tmux", "new-session", "-ds", to, "-n", windowName, "-c", targetPath, "bash", "-c", claudeCmd).CombinedOutput()
				if err != nil {
					database.Exec(`DELETE FROM claude_tasks WHERE id = ?`, taskID)
					return fmt.Errorf("tmux new-session: %s", strings.TrimSpace(string(out)))
				}
				fmt.Printf("cmdr: enlistment dispatched (task %d, squad %s, %s → %s)\n", taskID, squad, from, to)
				fmt.Printf("cmdr: branch %s, session %s:%s\n", branchName, to, windowName)
				notifyDaemon(squad, taskID)
				return nil
			}

			if out, err := exec.Command("tmux", tmuxArgs...).CombinedOutput(); err != nil {
				database.Exec(`DELETE FROM claude_tasks WHERE id = ?`, taskID)
				return fmt.Errorf("tmux new-window: %s", strings.TrimSpace(string(out)))
			}

			fmt.Printf("cmdr: enlistment dispatched (task %d, squad %s, %s → %s)\n", taskID, squad, from, to)
			fmt.Printf("cmdr: branch %s, session %s:%s\n", branchName, sessionName, windowName)
			notifyDaemon(squad, taskID)
			return nil
		},
	}
	cmd.Flags().StringVar(&squad, "squad", "", "Squad name")
	cmd.Flags().StringVar(&from, "from", "", "Requesting repo alias")
	cmd.Flags().StringVar(&to, "to", "", "Target repo alias")
	cmd.Flags().StringVar(&summary, "summary", "", "Brief description of what you need")
	cmd.Flags().StringVar(&details, "details", "", "Full specification")
	return cmd
}

// notifyDaemon publishes an SSE event via the daemon over the Unix socket.
func notifyDaemon(squad string, taskID int) {
	body, _ := json.Marshal(map[string]any{
		"event": "delegation:update",
		"data":  map[string]any{"squad": squad, "taskId": taskID, "status": "running"},
	})
	resp, err := daemon.Client().Post("http://cmdr/api/notify", "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
	}
}

// findSessionForRepo finds an existing tmux session that has a pane in the given directory.
func findSessionForRepo(repoPath string) string {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}\t#{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	resolved := repoPath
	if r, err := filepath.EvalSymlinks(repoPath); err == nil {
		resolved = r
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && (parts[1] == repoPath || parts[1] == resolved) {
			return parts[0]
		}
	}
	return ""
}

func missionsCmd() *cobra.Command {
	var repoPath string
	cmd := &cobra.Command{
		Use:   "missions",
		Short: "Check squad mission status (UserPromptSubmit hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoPath == "" {
				var err error
				repoPath, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
				repoPath = resolved
			}
			repoPath = filepath.Clean(repoPath)

			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()

			// Find this repo's squad alias
			var squadName, alias string
			database.QueryRow(`SELECT squad, squad_alias FROM repos WHERE path = ?`, repoPath).Scan(&squadName, &alias)
			if squadName == "" {
				// Not in a squad — nothing to check
				return outputHook("UserPromptSubmit", "")
			}

			// Find completed delegations from this repo that haven't been notified
			rows, err := database.Query(
				`SELECT ct.id, d.to_alias, COALESCE(ct.title, ''), COALESCE(ct.result, '')
				 FROM claude_tasks ct
				 JOIN delegations d ON d.task_id = ct.id
				 WHERE ct.type = 'delegation' AND d.squad = ? AND d.from_alias = ?
				   AND ct.status IN ('completed', 'done') AND d.notified = 0`,
				squadName, alias,
			)
			if err != nil {
				return outputHook("UserPromptSubmit", "")
			}
			defer rows.Close()

			var notifications []string
			var ids []int
			for rows.Next() {
				var id int
				var toAlias, title, result string
				rows.Scan(&id, &toAlias, &title, &result)
				ids = append(ids, id)
				msg := fmt.Sprintf("Enlistment complete: %s finished", toAlias)
				if title != "" {
					msg += fmt.Sprintf(" '%s'", title)
				}
				if result != "" {
					// Include the debrief summary (truncated for context injection)
					debrief := result
					if len(debrief) > 500 {
						debrief = debrief[:500] + "..."
					}
					msg += fmt.Sprintf("\n\nDebrief:\n%s", debrief)
				}
				notifications = append(notifications, msg)
			}

			if len(ids) == 0 {
				return outputHook("UserPromptSubmit", "")
			}

			// Mark as notified
			placeholders := make([]string, len(ids))
			notifyArgs := make([]any, len(ids))
			for i, id := range ids {
				placeholders[i] = "?"
				notifyArgs[i] = id
			}
			database.Exec(
				fmt.Sprintf(`UPDATE delegations SET notified = 1 WHERE task_id IN (%s)`, strings.Join(placeholders, ",")),
				notifyArgs...,
			)

			ctx := strings.Join(notifications, ". ")
			return outputHook("UserPromptSubmit", ctx)
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "Repository path (defaults to cwd)")
	return cmd
}

func outputHook(event, context string) error {
	type hookOutput struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = event
	out.HookSpecificOutput.AdditionalContext = context
	return json.NewEncoder(os.Stdout).Encode(out)
}
