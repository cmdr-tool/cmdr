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
// /api/graphs/:
//
//	GET    /api/graphs/{slug}/snapshots             → list snapshots
//	GET    /api/graphs/{slug}/context               → repo graph context
//	PUT    /api/graphs/{slug}/context               → update graph context
//	POST   /api/graphs/{slug}/build                 → kick off a graph build
//	GET    /api/graphs/{slug}/traces                → list traces
//	POST   /api/graphs/{slug}/traces                → create + generate {prompt}
//	POST   /api/graphs/{slug}/traces/{id}/regenerate→ regenerate trace
//	DELETE /api/graphs/{slug}/traces/{id}           → delete trace
//	GET    /api/graphs/{slug}/traces/events?trace_id=... → SSE feed for one trace
//	GET    /api/graphs/{slug}/{sha}                 → graph.json
//	GET    /api/graphs/{slug}/{sha}/report          → report.md
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
		case len(parts) == 2 && parts[1] == "traces" && r.Method == http.MethodGet:
			handleListTraces(database, parts[0])(w, r)
		case len(parts) == 2 && parts[1] == "traces" && r.Method == http.MethodPost:
			handleCreateTrace(database, store, parts[0])(w, r)
		case len(parts) == 3 && parts[1] == "traces" && parts[2] == "events" && r.Method == http.MethodGet:
			handleTraceEvents(parts[0])(w, r)
		case len(parts) == 3 && parts[1] == "traces" && r.Method == http.MethodDelete:
			handleDeleteTrace(database, parts[0], parts[2])(w, r)
		case len(parts) == 4 && parts[1] == "traces" && parts[3] == "regenerate" && r.Method == http.MethodPost:
			handleRegenerateTrace(database, store, parts[0], parts[2])(w, r)
		case len(parts) == 2:
			handleGetGraph(store, parts[0], parts[1])(w, r)
		case len(parts) == 3 && parts[2] == "report":
			handleGetGraphReport(store, parts[0], parts[1])(w, r)
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
// 'building' row and spawns the pipeline goroutine. The trace pipeline
// is independent of graph builds — see /traces endpoints.
//
// Query params:
//   - force=true    — skip the cached-snapshot short-circuit
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

		if gitlocal.DirtyWorkingTree(repoPath) {
			http.Error(w, `{"error":"working tree is dirty; commit or stash before building"}`, http.StatusConflict)
			return
		}

		sha, err := gitlocal.HeadSHA(repoPath)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		force := r.URL.Query().Get("force") == "true"
		snapshotID, status, err := kickOffGraphBuild(database, bus, store, slug, sha, repoPath, force)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		respondBuildAccepted(w, snapshotID, status)
	}
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
func kickOffGraphBuild(database *sql.DB, bus *EventBus, store *graph.Store, slug, sha, repoPath string, force bool) (snapshotID int64, status string, err error) {
	// Already-built SHA is a no-op unless force=true. The caller decides
	// via `force` whether stale extractor output is reason enough to
	// rebuild.
	var existingID int64
	row := database.QueryRow(`SELECT id FROM graph_snapshots WHERE repo_slug = ? AND commit_sha = ?`, slug, sha)
	if err := row.Scan(&existingID); err == nil && !force && store.HasSnapshot(slug, sha) {
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

	go runGraphBuild(database, bus, store, snapshotID, slug, sha, repoPath)
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
func runGraphBuild(database *sql.DB, bus *EventBus, store *graph.Store, snapshotID int64, slug, sha, repoPath string) {
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

// --- Traces endpoints ---

// traceRowJSON is the JSON wire shape returned by /api/graphs/{slug}/traces.
// Mirrors the persisted row but flattens the affected_files JSON column,
// adds a stale flag computed at view time, and ships current/previous
// snapshot SHAs (rather than ids) so the frontend can label versions
// against snapshot pickers.
type traceRowJSON struct {
	ID                    int64                    `json:"id"`
	RepoSlug              string                   `json:"repoSlug"`
	Prompt                string                   `json:"prompt"`
	Title                 string                   `json:"title"`
	AffectedFiles         []string                 `json:"affectedFiles"`
	Stale                 bool                     `json:"stale"`
	CurrentData           *graphtrace.Trace        `json:"currentData"`
	CurrentSnapshotSHA    string                   `json:"currentSnapshotSha,omitempty"`
	CurrentGeneratedAt    *string                  `json:"currentGeneratedAt"`
	CurrentStatus         string                   `json:"currentStatus"`
	CurrentError          string                   `json:"currentError,omitempty"`
	PreviousData          *graphtrace.Trace        `json:"previousData,omitempty"`
	PreviousSnapshotSHA   string                   `json:"previousSnapshotSha,omitempty"`
	PreviousGeneratedAt   *string                  `json:"previousGeneratedAt,omitempty"`
	PreviousChangeSummary *graphtrace.ChangeSummary `json:"previousChangeSummary,omitempty"`
	CreatedAt             string                   `json:"createdAt"`
}

// handleListTraces returns all traces for a repo, with stale flags
// computed against the current HEAD via cached git diffs.
func handleListTraces(database *sql.DB, slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repoPath, err := repoPathBySlug(database, slug)
		if err != nil {
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
			return
		}

		traces, err := graphtrace.ListByRepo(database, slug)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Resolve snapshot SHAs in bulk for any referenced ids.
		snapshotSHAs := map[int64]string{}
		for _, t := range traces {
			if t.CurrentSnapshotID != nil {
				snapshotSHAs[*t.CurrentSnapshotID] = ""
			}
			if t.PreviousSnapshotID != nil {
				snapshotSHAs[*t.PreviousSnapshotID] = ""
			}
		}
		if len(snapshotSHAs) > 0 {
			ids := make([]any, 0, len(snapshotSHAs))
			placeholders := make([]string, 0, len(snapshotSHAs))
			for id := range snapshotSHAs {
				ids = append(ids, id)
				placeholders = append(placeholders, "?")
			}
			q := `SELECT id, commit_sha FROM graph_snapshots WHERE id IN (` + strings.Join(placeholders, ",") + `)`
			rows, err := database.Query(q, ids...)
			if err == nil {
				for rows.Next() {
					var id int64
					var sha string
					if err := rows.Scan(&id, &sha); err == nil {
						snapshotSHAs[id] = sha
					}
				}
				rows.Close()
			}
		}

		// Batch the diff once per unique anchor SHA. Same-anchor traces
		// share the underlying diff result via the cache.
		diffByAnchor := map[string][]string{}
		for _, t := range traces {
			if t.CurrentSnapshotID == nil {
				continue
			}
			anchorSHA := snapshotSHAs[*t.CurrentSnapshotID]
			if anchorSHA == "" {
				continue
			}
			if _, ok := diffByAnchor[anchorSHA]; ok {
				continue
			}
			files, err := gitlocal.ChangedFilesSince(repoPath, anchorSHA)
			if err != nil {
				log.Printf("cmdr: trace stale check %s/%s: %v", slug, anchorSHA[:min(7, len(anchorSHA))], err)
				diffByAnchor[anchorSHA] = nil
				continue
			}
			diffByAnchor[anchorSHA] = files
		}

		out := make([]traceRowJSON, 0, len(traces))
		for _, t := range traces {
			row := traceRowJSON{
				ID:            t.ID,
				RepoSlug:      t.RepoSlug,
				Prompt:        t.Prompt,
				Title:         t.Title,
				AffectedFiles: t.AffectedFiles,
				CurrentData:   t.CurrentData,
				CurrentStatus: t.CurrentStatus,
				CurrentError:  t.CurrentError,
				PreviousData:  t.PreviousData,
				PreviousChangeSummary: t.PreviousChangeSummary,
				CreatedAt:     t.CreatedAt.Format(time.RFC3339),
			}
			if t.CurrentSnapshotID != nil {
				row.CurrentSnapshotSHA = snapshotSHAs[*t.CurrentSnapshotID]
			}
			if t.PreviousSnapshotID != nil {
				row.PreviousSnapshotSHA = snapshotSHAs[*t.PreviousSnapshotID]
			}
			if t.CurrentGeneratedAt != nil {
				v := t.CurrentGeneratedAt.Format(time.RFC3339)
				row.CurrentGeneratedAt = &v
			}
			if t.PreviousGeneratedAt != nil {
				v := t.PreviousGeneratedAt.Format(time.RFC3339)
				row.PreviousGeneratedAt = &v
			}
			if row.CurrentSnapshotSHA != "" {
				changed := diffByAnchor[row.CurrentSnapshotSHA]
				row.Stale = intersectFiles(t.AffectedFiles, changed)
			}
			out = append(out, row)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// intersectFiles returns true when affected and changed share any path.
// Both sides are typically small (single-digit to low-double-digit), so
// a nested loop is fine; we don't need a hash set.
func intersectFiles(affected, changed []string) bool {
	if len(affected) == 0 || len(changed) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(changed))
	for _, f := range changed {
		set[f] = struct{}{}
	}
	for _, f := range affected {
		if _, ok := set[f]; ok {
			return true
		}
	}
	return false
}

// handleCreateTrace inserts a new trace row in 'generating' state and
// kicks off the generation goroutine. The handler returns immediately
// once the row is durable so the UI can subscribe to /traces/events.
func handleCreateTrace(database *sql.DB, store *graph.Store, slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, jsonErr(err), http.StatusBadRequest)
				return
			}
		}
		body.Prompt = strings.TrimSpace(body.Prompt)
		if body.Prompt == "" {
			http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
			return
		}

		if _, err := repoPathBySlug(database, slug); err != nil {
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
			return
		}

		// A graph snapshot must exist before we can trace against it.
		snap, err := graphtrace.LoadLatestSnapshot(database, store, slug)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		if snap == nil {
			http.Error(w, `{"error":"no graph snapshot — build the graph first"}`, http.StatusConflict)
			return
		}

		title := generateTraceTitle(body.Prompt)

		traceID, err := graphtrace.Create(database, slug, body.Prompt, title)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		go runTraceGeneration(database, store, traceID, slug, body.Prompt)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"trace_id": traceID,
			"title":    title,
			"status":   "generating",
		})
	}
}

// handleRegenerateTrace flips the row back to 'generating' and runs the
// pipeline again. The version-flip rule (replace current vs promote to
// previous) is applied inside the goroutine after generation completes.
func handleRegenerateTrace(database *sql.DB, store *graph.Store, slug, idStr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID, err := parseTraceID(idStr)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		row, err := graphtrace.Get(database, traceID)
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"trace not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		if row.RepoSlug != slug {
			http.Error(w, `{"error":"trace does not belong to this repo"}`, http.StatusNotFound)
			return
		}

		if err := graphtrace.MarkGenerating(database, traceID); err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		go runTraceGeneration(database, store, traceID, slug, row.Prompt)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"trace_id": traceID,
			"status":   "generating",
		})
	}
}

// handleDeleteTrace removes a trace row. Idempotent — deleting a missing
// id returns 200 so the UI doesn't have to special-case races.
func handleDeleteTrace(database *sql.DB, slug, idStr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID, err := parseTraceID(idStr)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}
		// Verify the trace belongs to the slug so an unrelated client
		// can't delete by id alone.
		row, err := graphtrace.Get(database, traceID)
		if err == nil && row.RepoSlug != slug {
			http.Error(w, `{"error":"trace does not belong to this repo"}`, http.StatusNotFound)
			return
		}
		if err := graphtrace.Delete(database, traceID); err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		// Tear down any in-flight stream so subscribers see the close.
		graphtrace.Close(traceID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// handleTraceEvents serves the SSE feed for one trace's generation run.
// Per-trace channels survive page navigation and reconnects: the daemon
// publishes events regardless of who's subscribed, and late subscribers
// replay the buffered events on connect.
func handleTraceEvents(slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = slug // slug is used for routing only; the channel key is trace id
		idStr := r.URL.Query().Get("trace_id")
		traceID, err := parseTraceID(idStr)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher.Flush()

		ch, cleanup := graphtrace.Subscribe(traceID)
		defer cleanup()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(evt)
				if err != nil {
					continue
				}
				typ := evt.Type
				if typ == "" {
					typ = "message"
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
				flusher.Flush()
				if evt.Type == "phase" && (evt.Phase == graphtrace.PhaseDone || evt.Phase == graphtrace.PhaseFailed) {
					return
				}
			}
		}
	}
}

// runTraceGeneration is the background pipeline for create + regenerate.
// It resolves the latest snapshot, runs Generate, applies the version-flip
// rule (promote current → previous on snapshot change), and updates the
// row. Streams events via the per-trace pub/sub.
func runTraceGeneration(database *sql.DB, store *graph.Store, traceID int64, slug, prompt string) {
	graphtrace.Reset(traceID)
	defer graphtrace.Close(traceID)

	publish := func(e graphtrace.Event) {
		graphtrace.Publish(traceID, e)
	}
	publish(graphtrace.Event{Type: "phase", Phase: graphtrace.PhaseGenerating})

	snap, err := graphtrace.LoadLatestSnapshot(database, store, slug)
	if err != nil {
		failTraceRun(database, traceID, fmt.Errorf("resolve snapshot: %w", err), publish)
		return
	}
	if snap == nil {
		failTraceRun(database, traceID, fmt.Errorf("no graph snapshot — build the graph first"), publish)
		return
	}

	row, err := graphtrace.Get(database, traceID)
	if err != nil {
		failTraceRun(database, traceID, fmt.Errorf("load trace: %w", err), publish)
		return
	}

	ctx := context.Background()

	newTrace, affectedFiles, err := graphtrace.Generate(ctx, *snap, prompt, publish)
	if err != nil {
		failTraceRun(database, traceID, err, publish)
		return
	}

	// Same-snapshot regeneration: replace current outright. No previous flip.
	sameSnapshot := row.CurrentSnapshotID != nil && *row.CurrentSnapshotID == snap.ID
	if sameSnapshot || row.CurrentData == nil {
		if err := graphtrace.SetCurrentReady(database, traceID, *newTrace, snap.ID, affectedFiles); err != nil {
			failTraceRun(database, traceID, fmt.Errorf("save current: %w", err), publish)
			return
		}
		publish(graphtrace.Event{Type: "phase", Phase: graphtrace.PhaseDone})
		log.Printf("cmdr: trace[%d] %s: regenerated against same snapshot", traceID, slug)
		return
	}

	// Different snapshot: run comparison, then atomically promote
	// current → previous and write new current.
	publish(graphtrace.Event{Type: "phase", Phase: graphtrace.PhaseComparing})
	summary, err := graphtrace.Compare(ctx, *row.CurrentData, *newTrace, publish)
	if err != nil {
		// Soft fail: still publish the new trace; just attach an empty
		// summary so the UI can render the previous slot.
		log.Printf("cmdr: trace[%d] %s: compare failed: %v", traceID, slug, err)
		summary = &graphtrace.ChangeSummary{
			Summary: "Comparison unavailable.",
			Changes: []graphtrace.Change{},
		}
	}

	if err := graphtrace.PromoteCurrentToPrevious(database, traceID, *newTrace, snap.ID, affectedFiles, *summary); err != nil {
		failTraceRun(database, traceID, fmt.Errorf("promote: %w", err), publish)
		return
	}
	publish(graphtrace.Event{Type: "phase", Phase: graphtrace.PhaseDone})
	log.Printf("cmdr: trace[%d] %s: regenerated against new snapshot, %d changes",
		traceID, slug, len(summary.Changes))
}

func failTraceRun(database *sql.DB, traceID int64, cause error, publish func(graphtrace.Event)) {
	msg := cause.Error()
	if err := graphtrace.SetFailed(database, traceID, msg); err != nil {
		log.Printf("cmdr: trace[%d]: mark failed: %v", traceID, err)
	}
	publish(graphtrace.Event{Type: "error", Text: msg})
	publish(graphtrace.Event{Type: "phase", Phase: graphtrace.PhaseFailed})
	log.Printf("cmdr: trace[%d]: %v", traceID, cause)
}

// generateTraceTitle returns a short title for a trace prompt. Tries
// the configured summarizer adapter (Apple Intelligence → Ollama →
// snippet fallback) and falls back to the truncated prompt prefix on
// failure so the trace always has a non-empty title.
func generateTraceTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if sum != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		title, err := sum.Summarize(ctx, prompt)
		if err == nil {
			title = strings.TrimSpace(title)
			if title != "" {
				return title
			}
		}
	}
	return fallbackTitle(prompt)
}

func fallbackTitle(prompt string) string {
	const maxLen = 60
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "Untitled trace"
	}
	if len(prompt) <= maxLen {
		return prompt
	}
	return prompt[:maxLen] + "…"
}

func parseTraceID(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid trace id %q", s)
	}
	return id, nil
}
