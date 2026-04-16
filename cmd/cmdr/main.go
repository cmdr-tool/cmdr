package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	cmdr "github.com/mikehu/cmdr"
	"github.com/mikehu/cmdr/internal/daemon"
	"github.com/mikehu/cmdr/internal/db"
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
		Long: `Personal command runner and automation daemon.

For Claude sessions: enlistment status is delivered automatically via the
UserPromptSubmit hook on your next message — there is no command to poll
task or enlistment state. After running 'cmdr enlist', continue with work
that doesn't depend on the enlistment; completion will be injected into
your context when it lands.

'cmdr status' reports daemon status only (pid, task count). It does not
accept --task or --squad flags.`,
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
	root.AddCommand(taskCmd())

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

	// Desired hooks: event → list of commands
	desiredHooks := map[string][]string{
		"SessionStart": {
			fmt.Sprintf(`%s context --repo "${CLAUDE_PROJECT_DIR:-$PWD}"`, bin),
		},
		"UserPromptSubmit": {
			fmt.Sprintf(`%s missions --repo "${CLAUDE_PROJECT_DIR:-$PWD}"`, bin),
		},
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
	for event, cmds := range desiredHooks {
		hookList, _ := hooks[event].([]any)

		// Remove any old-format entries (flat {type, command} without hooks array)
		// and any stale cmdr entries (e.g. check-delegations, old paths)
		var cleaned []any
		for _, h := range hookList {
			hMap, ok := h.(map[string]any)
			if !ok {
				cleaned = append(cleaned, h)
				continue
			}
			// Old flat format: has "command" at top level but no "hooks" array
			if _, hasCmd := hMap["command"]; hasCmd {
				if _, hasHooks := hMap["hooks"]; !hasHooks {
					changed = true // dropping old-format entry
					continue
				}
			}
			// New format: check if any inner hook is a cmdr command
			if innerHooks, ok := hMap["hooks"].([]any); ok {
				isCmdr := false
				for _, ih := range innerHooks {
					if ihm, ok := ih.(map[string]any); ok {
						if cmd, _ := ihm["command"].(string); strings.Contains(cmd, "cmdr ") {
							isCmdr = true
							break
						}
					}
				}
				if isCmdr {
					changed = true // will be replaced below
					continue
				}
			}
			cleaned = append(cleaned, h)
		}

		// Build the cmdr entry with all commands in one hooks array
		var innerHooks []map[string]any
		for _, cmd := range cmds {
			innerHooks = append(innerHooks, map[string]any{
				"type":    "command",
				"command": cmd,
			})
		}
		entry := map[string]any{
			"matcher": "",
			"hooks":   innerHooks,
		}

		// Check if an identical entry already exists
		found := false
		for _, h := range cleaned {
			if hMap, ok := h.(map[string]any); ok {
				if existingHooks, ok := hMap["hooks"].([]any); ok && len(existingHooks) == len(innerHooks) {
					match := true
					for i, ih := range existingHooks {
						ihm, _ := ih.(map[string]any)
						if cmd, _ := ihm["command"].(string); cmd != cmds[i] {
							match = false
							break
						}
					}
					if match {
						found = true
						break
					}
				}
			}
		}

		if !found {
			cleaned = append(cleaned, entry)
			changed = true
		}

		hooks[event] = cleaned
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
		Long: `Enlist a squad member for cross-repo work.

Dispatches a task to a sibling repo's Claude session. After dispatch, do
NOT poll — there is no status command. Completion is delivered as context
on your next prompt via the UserPromptSubmit hook (including the debrief
written by the enlisted session). Continue with non-blocking work in the
meantime.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if squad == "" || from == "" || to == "" || summary == "" {
				return fmt.Errorf("--squad, --from, --to, and --summary are required")
			}

			body, _ := json.Marshal(map[string]string{
				"squad": squad, "from": from, "to": to,
				"summary": summary, "details": details,
			})
			resp, err := daemon.Client().Post("http://cmdr/api/squads/enlist", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("daemon unreachable: %w", err)
			}
			defer resp.Body.Close()

			var result map[string]any
			json.NewDecoder(resp.Body).Decode(&result)

			if resp.StatusCode != 200 {
				if errMsg, ok := result["error"].(string); ok {
					return fmt.Errorf("%s", errMsg)
				}
				return fmt.Errorf("enlist failed (status %d)", resp.StatusCode)
			}

			taskID := int(result["taskId"].(float64))
			branch := result["branch"].(string)
			session := result["session"].(string)
			fmt.Printf("cmdr: enlistment dispatched (task %d, squad %s, %s → %s)\n", taskID, squad, from, to)
			fmt.Printf("cmdr: branch %s, session %s\n", branch, session)
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

func taskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "task <id>",
		Short: "Show status and debrief for a task by ID",
		Long: `Show status and debrief for a task by ID.

Use this to actively check on an enlistment when you can't wait for the
UserPromptSubmit hook to deliver completion (e.g. mid-task in a headless
or autonomous run). The ID is the taskId returned by 'cmdr enlist'.

Status values: draft, running, completed, failed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()

			var id int
			if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
				return fmt.Errorf("invalid task id: %s", args[0])
			}

			var title, status, result, errMsg, intent string
			var squad, fromAlias, toAlias sql.NullString
			err = database.QueryRow(
				`SELECT ct.title, ct.status, ct.result, ct.error_msg, ct.intent,
				        d.squad, d.from_alias, d.to_alias
				 FROM claude_tasks ct
				 LEFT JOIN delegations d ON d.task_id = ct.id
				 WHERE ct.id = ?`, id,
			).Scan(&title, &status, &result, &errMsg, &intent, &squad, &fromAlias, &toAlias)
			if err == sql.ErrNoRows {
				return fmt.Errorf("task %d not found", id)
			}
			if err != nil {
				return err
			}

			fmt.Printf("Task %d: %s\n", id, title)
			fmt.Printf("Status: %s\n", status)
			if intent != "" {
				fmt.Printf("Intent: %s\n", intent)
			}
			if squad.Valid && squad.String != "" {
				fmt.Printf("Enlistment: squad=%s, %s → %s\n", squad.String, fromAlias.String, toAlias.String)
			}
			if errMsg != "" {
				fmt.Printf("\nError:\n%s\n", errMsg)
			}
			if result != "" {
				fmt.Printf("\nResult:\n%s\n", result)
			}
			return nil
		},
	}
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
