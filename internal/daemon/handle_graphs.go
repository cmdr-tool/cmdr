package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/gitlocal"
	"github.com/cmdr-tool/cmdr/internal/graph"
	"github.com/cmdr-tool/cmdr/internal/graphtrace"
)

// graphRepoRow is the per-repo summary returned from GET /api/graphs.
// Latest-snapshot fields are pointers so an unbuilt repo serializes as
// nulls rather than zero-valued strings/ints.
type graphRepoRow struct {
	RepoID          int     `json:"repoId"`
	RepoName        string  `json:"repoName"`
	RepoPath        string  `json:"repoPath"`
	Slug            string  `json:"slug"`
	SnapshotCount   int     `json:"snapshotCount"`
	LatestSHA       *string `json:"latestSha"`
	LatestBuiltAt   *string `json:"latestBuiltAt"`
	LatestStatus    *string `json:"latestStatus"`
	LatestNodeCount *int    `json:"latestNodeCount"`
}

// handleListGraphs returns one row per repo in the repos table with
// rollup info from graph_snapshots. The `monitor` flag is intentionally
// ignored here — it gates commit syncing, not graph eligibility. Repos
// with zero snapshots still appear, so the frontend can show a "Build
// graph" CTA on them.
func handleListGraphs(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := database.Query(`
			SELECT r.id, r.name, r.path,
			       (SELECT COUNT(*) FROM graph_snapshots g WHERE g.repo_path = r.path) AS snap_count,
			       (SELECT commit_sha FROM graph_snapshots g WHERE g.repo_path = r.path ORDER BY built_at DESC LIMIT 1),
			       (SELECT built_at   FROM graph_snapshots g WHERE g.repo_path = r.path ORDER BY built_at DESC LIMIT 1),
			       (SELECT status     FROM graph_snapshots g WHERE g.repo_path = r.path ORDER BY built_at DESC LIMIT 1),
			       (SELECT node_count FROM graph_snapshots g WHERE g.repo_path = r.path ORDER BY built_at DESC LIMIT 1)
			FROM repos r
			ORDER BY r.name
		`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := []graphRepoRow{}
		for rows.Next() {
			var row graphRepoRow
			var sha, builtAt, status sql.NullString
			var nodeCount sql.NullInt64
			if err := rows.Scan(&row.RepoID, &row.RepoName, &row.RepoPath, &row.SnapshotCount, &sha, &builtAt, &status, &nodeCount); err != nil {
				continue
			}
			row.Slug = graph.Slug(row.RepoPath)
			if sha.Valid {
				row.LatestSHA = &sha.String
			}
			if builtAt.Valid {
				row.LatestBuiltAt = &builtAt.String
			}
			if status.Valid {
				row.LatestStatus = &status.String
			}
			if nodeCount.Valid {
				n := int(nodeCount.Int64)
				row.LatestNodeCount = &n
			}
			out = append(out, row)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// handleGraphsSubpath dispatches the parameterized routes under
// /api/graphs/. Five shapes are accepted:
//
//	GET  /api/graphs/{slug}/snapshots       → [{commit_sha, built_at, ...}]
//	GET  /api/graphs/{slug}/context         → { context: "..." }
//	PUT  /api/graphs/{slug}/context         → update markdown context
//	GET  /api/graphs/{slug}/{sha}          → graph.json
//	GET  /api/graphs/{slug}/{sha}/report   → report.md
//	GET  /api/graphs/{slug}/{sha}/traces   → traces.json (404 if not generated)
//	POST /api/graphs/{slug}/{sha}/traces   → run trace pipeline, save, return result
//	POST /api/graphs/{slug}/build          → kick off a build
func handleGraphsSubpath(database *sql.DB, bus *EventBus, store *graph.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/graphs/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")

		switch {
		case len(parts) == 2 && parts[1] == "build":
			handleBuildGraph(database, bus, store, parts[0])(w, r)
		case len(parts) == 2 && parts[1] == "snapshots":
			handleListSnapshots(database, parts[0])(w, r)
		case len(parts) == 2 && parts[1] == "context":
			handleGraphContext(database, parts[0])(w, r)
		case len(parts) == 2:
			handleGetGraph(store, parts[0], parts[1])(w, r)
		case len(parts) == 3 && parts[2] == "report":
			handleGetGraphReport(store, parts[0], parts[1])(w, r)
		case len(parts) == 3 && parts[2] == "traces" && r.Method == http.MethodGet:
			handleGetTraces(store, parts[0], parts[1])(w, r)
		case len(parts) == 3 && parts[2] == "traces" && r.Method == http.MethodPost:
			handleGenerateTraces(database, store, parts[0], parts[1])(w, r)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
}

// handleGraphContext serves GET (returns current markdown context) and
// PUT (updates it) for a slug. Used by the /graphs UI to capture the
// per-repo guidance the LLM trace pipeline anchors against.
func handleGraphContext(database *sql.DB, slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repoPath, err := repoPathBySlug(database, slug)
		if err != nil {
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
			return
		}

		switch r.Method {
		case http.MethodGet:
			var ctx string
			if err := database.QueryRow(
				`SELECT graph_context FROM repos WHERE path = ?`, repoPath,
			).Scan(&ctx); err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"context": ctx})

		case http.MethodPut:
			var body struct {
				Context string `json:"context"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, jsonErr(err), http.StatusBadRequest)
				return
			}
			if _, err := database.Exec(
				`UPDATE repos SET graph_context = ? WHERE path = ?`,
				body.Context, repoPath,
			); err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true})

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// handleListSnapshots returns all snapshots for a slug, ordered by
// built_at DESC. Powers the snapshot picker in the viewer header.
func handleListSnapshots(database *sql.DB, slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := database.Query(`
			SELECT commit_sha, built_at, status,
			       COALESCE(node_count, 0), COALESCE(edge_count, 0),
			       COALESCE(community_count, 0), COALESCE(duration_ms, 0)
			FROM graph_snapshots
			WHERE repo_slug = ?
			ORDER BY built_at DESC
		`, slug)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type snap struct {
			CommitSHA      string `json:"commitSha"`
			BuiltAt        string `json:"builtAt"`
			Status         string `json:"status"`
			NodeCount      int    `json:"nodeCount"`
			EdgeCount      int    `json:"edgeCount"`
			CommunityCount int    `json:"communityCount"`
			DurationMs     int64  `json:"durationMs"`
		}
		out := []snap{}
		for rows.Next() {
			var s snap
			if err := rows.Scan(&s.CommitSHA, &s.BuiltAt, &s.Status, &s.NodeCount, &s.EdgeCount, &s.CommunityCount, &s.DurationMs); err != nil {
				continue
			}
			out = append(out, s)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// handleGetGraph streams the graph.json file for {slug}/{sha} verbatim.
func handleGetGraph(store *graph.Store, slug, sha string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(store.SnapshotDir(slug, sha), "graph.json")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, `{"error":"snapshot not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handleGetTraces returns the traces.json for a snapshot, or 404 if not
// generated yet. Frontend uses this to render the Traces facet.
func handleGetTraces(store *graph.Store, slug, sha string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := store.ReadTraces(slug, sha)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, `{"error":"traces not generated"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handleGenerateTraces synchronously runs the trace pipeline for a snapshot
// and writes traces.json to disk. The agent run can take a couple minutes;
// the request blocks until done. Returns the parsed result on success.
func handleGenerateTraces(database *sql.DB, store *graph.Store, slug, sha string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Guidance string `json:"guidance"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, jsonErr(err), http.StatusBadRequest)
				return
			}
		}

		// Verify the snapshot itself exists before kicking off work.
		if _, err := os.Stat(filepath.Join(store.SnapshotDir(slug, sha), "graph.json")); err != nil {
			http.Error(w, `{"error":"snapshot not found"}`, http.StatusNotFound)
			return
		}

		result, _, err := graphtrace.Run(r.Context(), database, store, slug, graphtrace.RunOptions{
			SnapshotSHA:  sha,
			UserGuidance: body.Guidance,
		}, nil)
		if err != nil {
			log.Printf("cmdr: trace generation failed for %s/%s: %v", slug, sha, err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		if err := result.Save(store); err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		data, err := json.Marshal(result)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handleGetGraphReport returns the markdown report.md for a snapshot.
func handleGetGraphReport(store *graph.Store, slug, sha string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(store.SnapshotDir(slug, sha), "report.md")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, `{"error":"report not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(data)
	}
}

// handleBuildGraph validates the slug, requires a clean working tree,
// no-ops when the HEAD sha already has a snapshot, otherwise inserts a
// 'building' row and spawns the pipeline goroutine.
//
// Query params:
//   - force=true    — skip the cached-snapshot short-circuit
//   - targets=...   — comma-separated set of stages to run; valid values
//                     are "graph" and "traces". Default is "graph". Use
//                     "graph,traces" to chain LLM tracing after the graph
//                     finishes, or "traces" to trace against the latest
//                     ready snapshot without rebuilding the graph.
func handleBuildGraph(database *sql.DB, bus *EventBus, store *graph.Store, slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		repoPath, err := repoPathBySlug(database, slug)
		if err != nil {
			http.Error(w, `{"error":"repo not monitored"}`, http.StatusNotFound)
			return
		}

		targets, err := parseBuildTargets(r.URL.Query().Get("targets"))
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		// Working-tree dirty check is only relevant when we're rebuilding
		// the graph from HEAD. Trace-only runs operate on the existing
		// snapshot and don't care about uncommitted work.
		if targets.graph && gitlocal.DirtyWorkingTree(repoPath) {
			http.Error(w, `{"error":"working tree is dirty; commit or stash before building"}`, http.StatusConflict)
			return
		}

		var sha string
		if targets.graph {
			sha, err = gitlocal.HeadSHA(repoPath)
			if err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
		} else {
			// traces-only path: target the most recent ready snapshot.
			err = database.QueryRow(
				`SELECT commit_sha FROM graph_snapshots
				 WHERE repo_slug = ? AND status = 'ready'
				 ORDER BY built_at DESC LIMIT 1`, slug,
			).Scan(&sha)
			if err == sql.ErrNoRows {
				http.Error(w, `{"error":"no ready snapshot to trace — build the graph first"}`, http.StatusConflict)
				return
			}
			if err != nil {
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
		}

		force := r.URL.Query().Get("force") == "true"
		snapshotID, status, err := kickOffGraphBuild(database, bus, store, slug, sha, repoPath, force, targets)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		respondBuildAccepted(w, snapshotID, status)
	}
}

// buildTargets describes which stages of the pipeline a build request wants.
type buildTargets struct {
	graph  bool
	traces bool
}

// parseBuildTargets reads the `targets` query param into a buildTargets
// struct. Empty input defaults to graph-only for backward compatibility.
// At least one stage must be selected.
func parseBuildTargets(raw string) (buildTargets, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return buildTargets{graph: true}, nil
	}
	t := buildTargets{}
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(part) {
		case "graph":
			t.graph = true
		case "traces":
			t.traces = true
		case "":
			// skip empty segments from things like "graph,,traces"
		default:
			return buildTargets{}, fmt.Errorf("unknown build target %q (valid: graph, traces)", part)
		}
	}
	if !t.graph && !t.traces {
		return buildTargets{}, fmt.Errorf("at least one target required (graph, traces)")
	}
	return t, nil
}

// kickOffGraphBuild is the build-orchestration core: returns existing
// snapshot id with status='ready' when the SHA already has a snapshot
// (and force is false), otherwise inserts/updates a 'building' row and
// spawns runGraphBuild as a goroutine. Used by both the HTTP handler
// and the scheduler graph-watch hook.
//
// force=true skips the cached-snapshot short-circuit. Use it when the
// user explicitly wants to rebuild — e.g. extractors changed since
// the last build and the current snapshot is stale even though the
// commit sha hasn't moved.
func kickOffGraphBuild(database *sql.DB, bus *EventBus, store *graph.Store, slug, sha, repoPath string, force bool, targets buildTargets) (snapshotID int64, status string, err error) {
	// Traces-only path: use the existing snapshot row, skip the graph build.
	if !targets.graph {
		var existingID int64
		err = database.QueryRow(`SELECT id FROM graph_snapshots WHERE repo_slug = ? AND commit_sha = ?`, slug, sha).Scan(&existingID)
		if err != nil {
			return 0, "", fmt.Errorf("traces-only: snapshot row lookup: %w", err)
		}
		go runTraceOnly(database, bus, store, existingID, slug, sha, repoPath)
		return existingID, "tracing", nil
	}

	// Already-built SHA is a no-op unless force=true OR traces are also
	// requested (in which case we run the trace step against it). The
	// caller decides via `force` whether stale extractor output is reason
	// enough to rebuild.
	var existingID int64
	row := database.QueryRow(`SELECT id FROM graph_snapshots WHERE repo_slug = ? AND commit_sha = ?`, slug, sha)
	if err := row.Scan(&existingID); err == nil && !force && store.HasSnapshot(slug, sha) {
		if targets.traces {
			// Graph is up-to-date; run traces against it.
			go runTraceOnly(database, bus, store, existingID, slug, sha, repoPath)
			return existingID, "tracing", nil
		}
		return existingID, "ready", nil
	}

	// Ensure the per-repo store directory exists before the goroutine
	// touches the cache subdirectory.
	if _, err := store.RepoDir(slug, repoPath); err != nil {
		return 0, "", err
	}

	now := time.Now().UTC()
	if existingID != 0 {
		// Stale row (file is gone): reset it for the fresh build.
		if _, err := database.Exec(
			`UPDATE graph_snapshots SET status='building', built_at=?, error='' WHERE id=?`,
			now, existingID,
		); err != nil {
			return 0, "", err
		}
		snapshotID = existingID
	} else {
		res, err := database.Exec(
			`INSERT INTO graph_snapshots (repo_path, repo_slug, commit_sha, built_at, status) VALUES (?, ?, ?, ?, 'building')`,
			repoPath, slug, sha, now,
		)
		if err != nil {
			return 0, "", err
		}
		snapshotID, _ = res.LastInsertId()
	}

	go runGraphBuild(database, bus, store, snapshotID, slug, sha, repoPath, targets.traces)
	return snapshotID, "building", nil
}

func respondBuildAccepted(w http.ResponseWriter, snapshotID int64, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"snapshot_id": snapshotID,
		"status":      status,
	})
}

// runGraphBuild executes the pipeline, publishes graphs:build events at
// each phase, and updates the graph_snapshots row with terminal state.
// When withTraces is true, the LLM trace pipeline runs after a successful
// graph build. Trace failures are soft — they're logged and surfaced via
// trace_error in the Complete event, but they don't fail the snapshot.
func runGraphBuild(database *sql.DB, bus *EventBus, store *graph.Store, snapshotID int64, slug, sha, repoPath string, withTraces bool) {
	publish := func(phase graph.Phase, percent int, extra map[string]any) {
		data := map[string]any{
			"snapshot_id": snapshotID,
			"slug":        slug,
			"sha":         sha,
			"phase":       string(phase),
			"percent":     percent,
		}
		for k, v := range extra {
			data[k] = v
		}
		bus.Publish(Event{Type: "graphs:build", Data: data})
	}

	publish(graph.PhaseStarted, 0, nil)
	started := time.Now().UTC()

	snap, err := graph.Build(graph.BuildOptions{
		RepoPath:  repoPath,
		CommitSHA: sha,
		Slug:      slug,
		Store:     store,
		OnProgress: func(p graph.Phase, pct int) {
			publish(p, pct, nil)
		},
	})
	if err != nil {
		failGraphBuild(database, snapshotID, err)
		publish(graph.PhaseFailed, 0, map[string]any{"error": err.Error()})
		return
	}

	snap.Snapshot.BuiltAt = started
	publish(graph.PhaseWriting, 90, nil)

	if err := store.WriteSnapshot(slug, sha, snap); err != nil {
		failGraphBuild(database, snapshotID, err)
		publish(graph.PhaseFailed, 0, map[string]any{"error": err.Error()})
		return
	}
	if err := store.WriteReport(slug, sha, graph.RenderReport(snap)); err != nil {
		// Soft fail: snapshot itself is durable; report is secondary.
		log.Printf("cmdr: graph: write report failed for %s/%s: %v", slug, sha, err)
	}

	duration := time.Since(started)
	log.Printf("cmdr: graph: built %s@%s — nodes=%d edges=%d communities=%d in %s",
		slug, sha[:min(7, len(sha))], snap.Stats.NodeCount, snap.Stats.EdgeCount, snap.Stats.CommunityCount, duration)

	completeExtra := map[string]any{
		"stats": map[string]any{
			"node_count":      snap.Stats.NodeCount,
			"edge_count":      snap.Stats.EdgeCount,
			"community_count": snap.Stats.CommunityCount,
			"duration_ms":     duration.Milliseconds(),
		},
	}

	// Run traces (if requested) BEFORE marking the snapshot 'ready' — keeps
	// the row in 'building' status throughout the whole pipeline so a page
	// reload can still detect the in-flight state from the DB. Trace
	// failures are soft: still mark 'ready', but surface trace_error.
	if withTraces {
		if traceErr := runTracesAfterBuild(database, bus, store, snapshotID, slug, sha, publish); traceErr != nil {
			completeExtra["trace_error"] = traceErr.Error()
		}
	}

	if _, err := database.Exec(
		`UPDATE graph_snapshots
		   SET status='ready', node_count=?, edge_count=?, community_count=?, duration_ms=?, error=''
		 WHERE id=?`,
		snap.Stats.NodeCount, snap.Stats.EdgeCount, snap.Stats.CommunityCount, duration.Milliseconds(), snapshotID,
	); err != nil {
		log.Printf("cmdr: graph: update row failed for snapshot %d: %v", snapshotID, err)
	}

	publish(graph.PhaseComplete, 100, completeExtra)
}

// runTraceOnly is the entrypoint for traces-only builds (targets=traces).
// It emits a started → tracing → complete event sequence without touching
// the graph_snapshots row's state machine.
func runTraceOnly(database *sql.DB, bus *EventBus, store *graph.Store, snapshotID int64, slug, sha, repoPath string) {
	publish := func(phase graph.Phase, percent int, extra map[string]any) {
		data := map[string]any{
			"snapshot_id": snapshotID,
			"slug":        slug,
			"sha":         sha,
			"phase":       string(phase),
			"percent":     percent,
		}
		for k, v := range extra {
			data[k] = v
		}
		bus.Publish(Event{Type: "graphs:build", Data: data})
	}

	publish(graph.PhaseStarted, 0, nil)
	completeExtra := map[string]any{}
	if err := runTracesAfterBuild(database, bus, store, snapshotID, slug, sha, publish); err != nil {
		completeExtra["trace_error"] = err.Error()
	}
	publish(graph.PhaseComplete, 100, completeExtra)
}

// runTracesAfterBuild runs the trace pipeline against an existing snapshot
// and saves traces.json. Emits PhaseTracing before invoking the agent.
// Returns nil on success; on failure, returns the error to be surfaced in
// the caller's Complete event payload.
func runTracesAfterBuild(database *sql.DB, bus *EventBus, store *graph.Store, snapshotID int64, slug, sha string, publish func(graph.Phase, int, map[string]any)) error {
	publish(graph.PhaseTracing, 95, nil)
	started := time.Now().UTC()
	result, _, err := graphtrace.Run(context.Background(), database, store, slug, graphtrace.RunOptions{
		SnapshotSHA: sha,
	}, func(line string) {
		// Forward each agent step as a tracing event so the UI can show
		// progress while the LLM grinds. Also log to stderr for postmortems.
		log.Printf("cmdr: trace[%s/%s]: %s", slug, sha[:min(7, len(sha))], line)
		publish(graph.PhaseTracing, 95, map[string]any{"detail": line})
	})
	if err != nil {
		log.Printf("cmdr: trace failed for %s/%s: %v", slug, sha, err)
		return err
	}
	// graphtrace.Run already validated, promoted, and zero-checked. We
	// don't need to call result.Save — the agent wrote the file and Run
	// atomically renamed it to the canonical path on success.
	log.Printf("cmdr: trace: built %s@%s — %d flows in %s",
		slug, sha[:min(7, len(sha))], len(result.Traces), time.Since(started))
	return nil
}

func failGraphBuild(database *sql.DB, snapshotID int64, cause error) {
	if _, err := database.Exec(
		`UPDATE graph_snapshots SET status='failed', error=? WHERE id=?`,
		cause.Error(), snapshotID,
	); err != nil {
		log.Printf("cmdr: graph: failed to mark snapshot %d as failed: %v", snapshotID, err)
	}
}

// repoPathBySlug walks all repos and returns the one whose derived
// slug matches. Slugs are derived from the absolute path so they're
// stable without storing them on the repos row. The `monitor` flag is
// not consulted — graph builds are user-triggered, independent of
// whether the repo is being fetched on the sync schedule.
func repoPathBySlug(database *sql.DB, slug string) (string, error) {
	rows, err := database.Query(`SELECT path FROM repos`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		if graph.Slug(p) == slug {
			return p, nil
		}
	}
	return "", fmt.Errorf("graph: slug %q not found", slug)
}
