package graphtrace

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/agentoverride"
	"github.com/cmdr-tool/cmdr/internal/graph"
)

//go:embed system_prompt.md
var defaultSystemPrompt string

//go:embed user_prompt.md
var userPromptSrc string

var userPromptTemplate = template.Must(template.New("user_prompt.md").Parse(userPromptSrc))

// Inputs gathered from DB + graph store before invoking the agent.
type runInputs struct {
	RepoSlug     string
	RepoPath     string
	CommitSHA    string
	GraphPath    string
	OutputPath   string // where the agent must Write the final traces.json
	RepoContext  string
	UserGuidance string
}

// RunOptions configures one trace generation run.
type RunOptions struct {
	SnapshotSHA  string
	UserGuidance string
}

// Run loads inputs, builds the prompt, invokes the agent, and parses the
// JSON block from its output. onProgress receives human-readable progress
// lines (typically the caller writes them to stderr).
//
// The trace pipeline defaults to the Claude adapter, but ~/.cmdr/agents/trace.md
// can override both the agent and the system prompt.
func Run(ctx context.Context, database *sql.DB, store *graph.Store, slug string, opts RunOptions, onProgress func(string)) (*Result, string, error) {
	inputs, err := loadInputs(database, store, slug, opts.SnapshotSHA, opts.UserGuidance)
	if err != nil {
		return nil, "", err
	}

	userPrompt, err := renderUserPrompt(inputs)
	if err != nil {
		return nil, "", fmt.Errorf("render user prompt: %w", err)
	}

	// Override resolution: ~/.cmdr/agents/trace.md (if present) supplies
	// the agent + system prompt. Fall back to the default Claude adapter
	// and the embedded system_prompt.md.
	a, systemPrompt, _ := agentoverride.Resolve("trace", "claude")
	if a == nil {
		return nil, "", fmt.Errorf("no usable agent for trace run")
	}
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultSystemPrompt
	}

	if onProgress == nil {
		onProgress = func(string) {}
	}

	// Don't set AllowedTools — match the convention used by review/analysis/
	// ask flows (see runHeadlessStreaming). Each agent has its own default
	// tool set; restricting to specific names ("Read", "Grep", "Glob") was
	// an over-defensive choice that broke pi (its CLI doesn't recognize
	// those tool names, so it ran with zero tools and produced empty
	// traces). The prompt itself directs the agent to read files; we trust
	// it not to write them.
	streamRes, err := a.RunStreaming(ctx, agent.StreamingConfig{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		WorkDir:      inputs.RepoPath,
	}, func(e agent.StreamEvent) {
		switch e.Type {
		case "tool":
			if e.Detail != "" {
				onProgress(fmt.Sprintf("· %s: %s", e.Tool, e.Detail))
			} else {
				onProgress(fmt.Sprintf("· %s", e.Tool))
			}
		case "text":
			snippet := strings.TrimSpace(e.Text)
			if len(snippet) > 120 {
				snippet = snippet[:117] + "..."
			}
			if snippet != "" {
				onProgress("  " + snippet)
			}
		case "error":
			onProgress("! " + e.Text)
		}
	})
	if err != nil {
		return nil, "", err
	}

	// Load the artifact the agent wrote via its Write tool. The agent is
	// told to write to a .tmp path; we validate it parses and has at least
	// one trace, then atomically rename to the canonical traces.json. A
	// failed run leaves the .tmp on disk for debugging without corrupting
	// any prior good traces.json.
	tmpPath := inputs.OutputPath
	finalPath := store.TracesPath(inputs.RepoSlug, inputs.CommitSHA)

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, streamRes.Output, fmt.Errorf("agent did not write %s — verify the system prompt instructs use of the Write tool", tmpPath)
		}
		return nil, streamRes.Output, fmt.Errorf("read tmp traces: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, streamRes.Output, fmt.Errorf("invalid JSON at %s: %w", tmpPath, err)
	}
	if len(result.Traces) == 0 {
		// Soft fail — leave .tmp for inspection, don't promote, don't
		// touch the prior traces.json.
		return nil, streamRes.Output, fmt.Errorf("agent produced 0 flows — check that the configured agent can use Read/Grep/Glob and is following the schema")
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, streamRes.Output, fmt.Errorf("promote traces (%s -> %s): %w", tmpPath, finalPath, err)
	}
	result.RepoSlug = inputs.RepoSlug
	result.CommitSHA = inputs.CommitSHA
	return &result, streamRes.Output, nil
}

func loadInputs(database *sql.DB, store *graph.Store, slug, requestedSHA, guidance string) (*runInputs, error) {
	var repoPath, repoContext string
	rows, err := database.Query(`SELECT path, graph_context FROM repos`)
	if err != nil {
		return nil, fmt.Errorf("query repos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p, gc string
		if err := rows.Scan(&p, &gc); err != nil {
			continue
		}
		if graph.Slug(p) == slug {
			repoPath = p
			repoContext = gc
			break
		}
	}
	if repoPath == "" {
		return nil, fmt.Errorf("slug %q not found in repos", slug)
	}

	sha := requestedSHA
	if sha == "" {
		// No explicit SHA — pick the latest snapshot whose graph artifact
		// is on disk. Status='ready' is the strongest signal, but the
		// trace pipeline now keeps status='building' through its own run,
		// so we accept either as long as graph.json exists (checked below).
		err = database.QueryRow(
			`SELECT commit_sha FROM graph_snapshots
			 WHERE repo_slug = ? AND status IN ('ready', 'building')
			 ORDER BY built_at DESC LIMIT 1`, slug,
		).Scan(&sha)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no usable snapshot for slug %q — build the graph first", slug)
		}
		if err != nil {
			return nil, fmt.Errorf("query snapshot: %w", err)
		}
	} else {
		var status string
		err = database.QueryRow(
			`SELECT status FROM graph_snapshots WHERE repo_slug = ? AND commit_sha = ? LIMIT 1`, slug, sha,
		).Scan(&status)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("snapshot %q not found for slug %q", sha, slug)
		}
		if err != nil {
			return nil, fmt.Errorf("query snapshot %q: %w", sha, err)
		}
		// Accept 'building' too — when the build pipeline chains traces,
		// the row is still in 'building' status because we don't flip to
		// 'ready' until after traces complete. graph.json existence is the
		// real readiness signal (verified below).
		if status != "ready" && status != "building" {
			return nil, fmt.Errorf("snapshot %q for slug %q is %s, not usable", sha, slug, status)
		}
	}

	graphPath := filepath.Join(store.SnapshotDir(slug, sha), "graph.json")
	if _, err := os.Stat(graphPath); err != nil {
		return nil, fmt.Errorf("graph.json missing at %s: %w", graphPath, err)
	}

	if strings.TrimSpace(repoContext) == "" {
		repoContext = "(none — the user has not provided a context.md for this repo. Infer flows from the graph and code alone.)"
	}

	return &runInputs{
		RepoSlug: slug,
		RepoPath: repoPath,
		CommitSHA: sha,
		GraphPath: graphPath,
		// Agent writes to a .tmp path; we validate and atomically rename
		// to the canonical traces.json on success. A failed run leaves
		// the .tmp on disk (for debugging) and the previous traces.json
		// (if any) untouched.
		OutputPath:   store.TracesPath(slug, sha) + ".tmp",
		RepoContext:  repoContext,
		UserGuidance: strings.TrimSpace(guidance),
	}, nil
}

func renderUserPrompt(in *runInputs) (string, error) {
	var buf bytes.Buffer
	if err := userPromptTemplate.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Save marshals the result and writes it to traces.json under the slug+sha
// directory. The graph.Store owns the path — graphtrace owns the marshaling.
func (r *Result) Save(store *graph.Store) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal traces: %w", err)
	}
	return store.WriteTraces(r.RepoSlug, r.CommitSHA, data)
}

// Load reads and parses traces.json for a snapshot.
func Load(store *graph.Store, slug, sha string) (*Result, error) {
	data, err := store.ReadTraces(slug, sha)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("unmarshal traces: %w", err)
	}
	return &r, nil
}

