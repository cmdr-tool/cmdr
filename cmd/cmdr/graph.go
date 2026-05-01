package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	_ "github.com/cmdr-tool/cmdr/internal/agent/claude" // register adapter
	_ "github.com/cmdr-tool/cmdr/internal/agent/pi"     // register adapter
	"github.com/cmdr-tool/cmdr/internal/agentoverride"
	"github.com/cmdr-tool/cmdr/internal/db"
	"github.com/cmdr-tool/cmdr/internal/graph"
	"github.com/cmdr-tool/cmdr/internal/graphtrace"
	"github.com/spf13/cobra"
)

func graphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Knowledge graph commands",
	}
	cmd.AddCommand(graphTraceCmd())
	return cmd
}

func graphTraceCmd() *cobra.Command {
	var slug, sha, guidance string
	var showRaw, save bool
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Generate LLM-augmented data flow traces for a repo graph snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if slug == "" {
				return fmt.Errorf("--repo is required (slug, e.g. workers-a1b2c3)")
			}

			database, err := db.Open()
			if err != nil {
				return err
			}
			defer database.Close()

			store, err := graph.NewStore()
			if err != nil {
				return err
			}

			// Load ~/.cmdr/agents/*.md so a `trace.md` override (if present)
			// can substitute its agent + system prompt for this run.
			agentoverride.Load()

			fmt.Fprintf(os.Stderr, "cmdr: tracing %s...\n", slug)
			result, raw, err := graphtrace.Run(cmd.Context(), database, store, slug, graphtrace.RunOptions{
				SnapshotSHA:  sha,
				UserGuidance: guidance,
			}, func(line string) {
				fmt.Fprintln(os.Stderr, line)
			})
			if showRaw {
				fmt.Fprintln(os.Stderr, "--- raw agent output ---")
				fmt.Fprintln(os.Stderr, raw)
				fmt.Fprintln(os.Stderr, "--- end raw output ---")
			}
			if err != nil {
				return err
			}

			if save {
				if err := result.Save(store); err != nil {
					return fmt.Errorf("save traces: %w", err)
				}
				fmt.Fprintf(os.Stderr, "cmdr: saved %s\n", store.TracesPath(result.RepoSlug, result.CommitSHA))
			}

			out, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&slug, "repo", "", "Repo slug (required)")
	cmd.Flags().StringVar(&sha, "sha", "", "Specific snapshot commit SHA (defaults to latest ready snapshot)")
	cmd.Flags().StringVar(&guidance, "guidance", "", "Optional user guidance for what flows/traces to generate")
	cmd.Flags().BoolVar(&showRaw, "raw", false, "Print raw agent output to stderr for debugging")
	cmd.Flags().BoolVar(&save, "save", false, "Persist traces.json next to the graph snapshot")
	cmd.SetContext(context.Background())
	return cmd
}
