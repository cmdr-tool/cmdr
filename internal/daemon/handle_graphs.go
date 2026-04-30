package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/gitlocal"
	"github.com/cmdr-tool/cmdr/internal/graph"
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
// /api/graphs/. Four shapes are accepted:
//
//	GET  /api/graphs/{slug}/snapshots       → [{commit_sha, built_at, ...}]
//	GET  /api/graphs/{slug}/{sha}          → graph.json
//	GET  /api/graphs/{slug}/{sha}/report   → report.md
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
		case len(parts) == 2:
			handleGetGraph(store, parts[0], parts[1])(w, r)
		case len(parts) == 3 && parts[2] == "report":
			handleGetGraphReport(store, parts[0], parts[1])(w, r)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
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
// 'building' row and spawns the pipeline goroutine.
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

		snapshotID, status, err := kickOffGraphBuild(database, bus, store, slug, sha, repoPath)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		respondBuildAccepted(w, snapshotID, status)
	}
}

// kickOffGraphBuild is the build-orchestration core: returns existing
// snapshot id with status='ready' when the SHA already has a snapshot,
// otherwise inserts/updates a 'building' row and spawns runGraphBuild
// as a goroutine. Used by both the HTTP handler and the scheduler
// graph-watch hook.
func kickOffGraphBuild(database *sql.DB, bus *EventBus, store *graph.Store, slug, sha, repoPath string) (snapshotID int64, status string, err error) {
	// Already-built SHA is a no-op: return the existing row id.
	var existingID int64
	row := database.QueryRow(`SELECT id FROM graph_snapshots WHERE repo_slug = ? AND commit_sha = ?`, slug, sha)
	if err := row.Scan(&existingID); err == nil && store.HasSnapshot(slug, sha) {
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
	if _, err := database.Exec(
		`UPDATE graph_snapshots
		   SET status='ready', node_count=?, edge_count=?, community_count=?, duration_ms=?, error=''
		 WHERE id=?`,
		snap.Stats.NodeCount, snap.Stats.EdgeCount, snap.Stats.CommunityCount, duration.Milliseconds(), snapshotID,
	); err != nil {
		log.Printf("cmdr: graph: update row failed for snapshot %d: %v", snapshotID, err)
	}

	log.Printf("cmdr: graph: built %s@%s — nodes=%d edges=%d communities=%d in %s",
		slug, sha[:min(7, len(sha))], snap.Stats.NodeCount, snap.Stats.EdgeCount, snap.Stats.CommunityCount, duration)

	publish(graph.PhaseComplete, 100, map[string]any{
		"stats": map[string]any{
			"node_count":      snap.Stats.NodeCount,
			"edge_count":      snap.Stats.EdgeCount,
			"community_count": snap.Stats.CommunityCount,
			"duration_ms":     duration.Milliseconds(),
		},
	})
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

