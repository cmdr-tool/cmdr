package main

import (
	"fmt"
	"io/fs"
	"os"

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
		Version: version,
	}

	root.AddCommand(startCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(runCmd())
	root.AddCommand(listCmd())

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
			s := scheduler.New(database)
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
			s := scheduler.New(database)
			for _, t := range s.Tasks() {
				fmt.Printf("  %-20s %s\t%s\n", t.Name, t.Schedule, t.Description)
			}
			return nil
		},
	}
}
