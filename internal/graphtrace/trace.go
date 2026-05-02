package graphtrace

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/agentoverride"
	"github.com/cmdr-tool/cmdr/internal/graph"
)

//go:embed generate_system_prompt.md
var generateSystemPrompt string

//go:embed generate_user_prompt.md
var generateUserPromptSrc string

//go:embed compare_system_prompt.md
var compareSystemPrompt string

//go:embed compare_user_prompt.md
var compareUserPromptSrc string

var (
	generateUserPromptTemplate = template.Must(template.New("generate_user_prompt.md").Parse(generateUserPromptSrc))
	compareUserPromptTemplate  = template.Must(template.New("compare_user_prompt.md").Parse(compareUserPromptSrc))
)

// Snapshot identifies the graph snapshot a generation run anchors against.
// The caller resolves this — Generate doesn't query the DB.
type Snapshot struct {
	ID        int64  // graph_snapshots.id
	Slug      string // repo slug
	CommitSHA string // anchored commit
	RepoPath  string // absolute path to the working repo
	GraphPath string // absolute path to graph.json
}

// LoadLatestSnapshot fetches the most recent ready/building snapshot for
// a repo slug, returning nil when there is no usable snapshot. Used by
// the create/regenerate handlers to decide what the new trace anchors
// against.
func LoadLatestSnapshot(database *sql.DB, store *graph.Store, slug string) (*Snapshot, error) {
	var (
		id       int64
		sha      string
		repoPath string
	)
	err := database.QueryRow(
		`SELECT id, commit_sha, repo_path
		   FROM graph_snapshots
		  WHERE repo_slug = ? AND status IN ('ready', 'building')
		  ORDER BY built_at DESC LIMIT 1`,
		slug,
	).Scan(&id, &sha, &repoPath)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query latest snapshot for %q: %w", slug, err)
	}

	graphPath := filepath.Join(store.SnapshotDir(slug, sha), "graph.json")
	if _, err := os.Stat(graphPath); err != nil {
		return nil, fmt.Errorf("graph.json missing at %s: %w", graphPath, err)
	}
	return &Snapshot{
		ID:        id,
		Slug:      slug,
		CommitSHA: sha,
		RepoPath:  repoPath,
		GraphPath: graphPath,
	}, nil
}

// Generate runs the generation LLM call against a snapshot using the user's
// prompt and returns the parsed Trace plus its computed affected_files. The
// onEvent callback receives streaming events from the agent (forwarded by
// the daemon to the SSE channel for live UI feedback).
func Generate(ctx context.Context, snap Snapshot, userPrompt string, onEvent func(Event)) (*Trace, []string, error) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}

	// graph.json is large (often 100KB+) — pass the path and let the
	// agent Read it selectively rather than inlining the whole thing
	// in the prompt. Inlining trips agent input limits and (for pi
	// v0.70.6) caused stack-overflow crashes during prompt parsing.
	rendered, err := renderTemplate(generateUserPromptTemplate, map[string]any{
		"UserPrompt": strings.TrimSpace(userPrompt),
		"RepoSlug":   snap.Slug,
		"RepoPath":   snap.RepoPath,
		"GraphPath":  snap.GraphPath,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("render generate user prompt: %w", err)
	}

	a, sysPrompt, _ := agentoverride.Resolve("trace", "claude")
	if a == nil {
		return nil, nil, fmt.Errorf("no usable agent for trace generation")
	}
	if strings.TrimSpace(sysPrompt) == "" {
		sysPrompt = generateSystemPrompt
	}

	streamRes, err := a.RunStreaming(ctx, agent.StreamingConfig{
		Prompt:       rendered,
		SystemPrompt: sysPrompt,
		WorkDir:      snap.RepoPath,
	}, func(e agent.StreamEvent) {
		switch e.Type {
		case "tool":
			onEvent(Event{Type: "tool", Tool: e.Tool, Detail: e.Detail})
		case "text":
			snippet := strings.TrimSpace(e.Text)
			if snippet != "" {
				onEvent(Event{Type: "text", Text: snippet})
			}
		case "error":
			onEvent(Event{Type: "error", Text: e.Text})
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("agent run: %w", err)
	}

	trace, err := parseTraceJSON(streamRes.Output)
	if err != nil {
		return nil, nil, err
	}
	if len(trace.Steps) == 0 {
		return nil, nil, fmt.Errorf("agent produced empty trace — check that the agent followed the schema")
	}

	files := collectAffectedFiles(trace)
	return trace, files, nil
}

// Compare runs the comparison LLM call to produce a structured ChangeSummary
// describing differences between two trace versions. No tool exploration —
// the call is read-only over the two JSON inputs.
func Compare(ctx context.Context, prev, curr Trace, onEvent func(Event)) (*ChangeSummary, error) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}

	prevJSON, err := json.MarshalIndent(prev, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal previous: %w", err)
	}
	currJSON, err := json.MarshalIndent(curr, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal current: %w", err)
	}

	rendered, err := renderTemplate(compareUserPromptTemplate, map[string]any{
		"PreviousTraceJSON": string(prevJSON),
		"CurrentTraceJSON":  string(currJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("render compare user prompt: %w", err)
	}

	a, sysPrompt, _ := agentoverride.Resolve("trace-compare", "claude")
	if a == nil {
		return nil, fmt.Errorf("no usable agent for trace comparison")
	}
	if strings.TrimSpace(sysPrompt) == "" {
		sysPrompt = compareSystemPrompt
	}

	streamRes, err := a.RunStreaming(ctx, agent.StreamingConfig{
		Prompt:       rendered,
		SystemPrompt: sysPrompt,
	}, func(e agent.StreamEvent) {
		switch e.Type {
		case "tool":
			onEvent(Event{Type: "tool", Tool: e.Tool, Detail: e.Detail})
		case "text":
			snippet := strings.TrimSpace(e.Text)
			if snippet != "" {
				onEvent(Event{Type: "text", Text: snippet})
			}
		case "error":
			onEvent(Event{Type: "error", Text: e.Text})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("compare agent run: %w", err)
	}

	summary, err := parseChangeSummaryJSON(streamRes.Output)
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// collectAffectedFiles walks the trace's steps and requirements and returns
// the deduplicated, sorted list of source files mentioned. Used to support
// view-time staleness detection: when HEAD has moved past the snapshot, any
// trace whose affected_files intersect the diff is flagged stale.
func collectAffectedFiles(t *Trace) []string {
	seen := map[string]struct{}{}
	for _, st := range t.Steps {
		if f := strings.TrimSpace(st.SourceFile); f != "" {
			seen[f] = struct{}{}
		}
		for _, req := range st.Requires {
			if f := strings.TrimSpace(req.SourceFile); f != "" {
				seen[f] = struct{}{}
			}
		}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// parseTraceJSON pulls the JSON object out of an agent's text response.
// The system prompt commands JSON-only, but agents sometimes wrap output
// in fences or trailing commentary; we tolerate that to avoid a hard
// failure on a recoverable lapse.
func parseTraceJSON(raw string) (*Trace, error) {
	body, err := extractJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("locate JSON in agent output: %w", err)
	}
	var t Trace
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return nil, fmt.Errorf("invalid trace JSON: %w (raw: %s)", err, truncate(body, 500))
	}
	return &t, nil
}

func parseChangeSummaryJSON(raw string) (*ChangeSummary, error) {
	body, err := extractJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("locate JSON in agent output: %w", err)
	}
	var cs ChangeSummary
	if err := json.Unmarshal([]byte(body), &cs); err != nil {
		return nil, fmt.Errorf("invalid change-summary JSON: %w (raw: %s)", err, truncate(body, 500))
	}
	return &cs, nil
}

// extractJSONObject finds the first balanced JSON object in s and returns
// its source. Tolerates leading/trailing prose or markdown fences.
func extractJSONObject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty agent output")
	}

	// Strip a markdown fence if present.
	if strings.HasPrefix(s, "```") {
		// drop the first line
		if nl := strings.Index(s, "\n"); nl >= 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}

	start := strings.Index(s, "{")
	if start < 0 {
		return "", fmt.Errorf("no '{' found in agent output")
	}

	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced JSON object in agent output")
}

func renderTemplate(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
