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
	var slug, prompt string
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Generate a single per-flow trace via the LLM (debug helper)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if slug == "" {
				return fmt.Errorf("--repo is required (slug, e.g. workers-a1b2c3)")
			}
			if prompt == "" {
				return fmt.Errorf("--prompt is required (the flow to trace)")
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

			snap, err := graphtrace.LoadLatestSnapshot(database, store, slug)
			if err != nil {
				return err
			}
			if snap == nil {
				return fmt.Errorf("no usable snapshot for slug %q — build the graph first", slug)
			}

			fmt.Fprintf(os.Stderr, "cmdr: tracing %s @ %s...\n", slug, snap.CommitSHA[:7])
			trace, files, err := graphtrace.Generate(cmd.Context(), *snap, prompt, func(e graphtrace.Event) {
				switch e.Type {
				case "tool":
					if e.Detail != "" {
						fmt.Fprintf(os.Stderr, "· %s: %s\n", e.Tool, e.Detail)
					} else {
						fmt.Fprintf(os.Stderr, "· %s\n", e.Tool)
					}
				case "text":
					fmt.Fprintf(os.Stderr, "  %s\n", e.Text)
				case "error":
					fmt.Fprintf(os.Stderr, "! %s\n", e.Text)
				}
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "cmdr: %d steps, %d affected files\n", len(trace.Steps), len(files))

			out, err := json.MarshalIndent(trace, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&slug, "repo", "", "Repo slug (required)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "User prompt describing the flow to trace (required)")
	cmd.SetContext(context.Background())
	return cmd
}
