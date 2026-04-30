package tasks

import (
	"database/sql"
	"log"
	"os"

	"github.com/cmdr-tool/cmdr/internal/gitlocal"
)

// GraphWatchHook is invoked by the graph-watch task when it decides
// a repo's HEAD has moved past the last built snapshot. The hook is
// expected to drive the actual build (the daemon owns the pipeline
// orchestration; tasks don't import daemon to avoid cycles).
//
// The hook receives the repo's slug, the new HEAD sha, and the
// repo path. The hook is responsible for any further validation and
// for spawning the build goroutine.
type GraphWatchHook func(slug, sha, repoPath string)

// GraphWatch returns a task function that walks repos with at least
// one ready snapshot, checks if HEAD has moved past the latest stored
// SHA, and invokes hook to trigger a rebuild for each that has.
//
// Skips silently when:
//   - repo directory no longer exists on disk
//   - working tree is dirty (per gitlocal.DirtyWorkingTree semantics)
//   - HEAD matches the latest stored SHA already
//   - reading git fails for any reason
//
// Repos with zero prior snapshots are skipped — initial builds remain
// explicit user actions per the ADR design.
func GraphWatch(db *sql.DB, hook GraphWatchHook) func() error {
	return func() error {
		if hook == nil {
			return nil
		}

		// Repos with at least one ready snapshot, picking the latest sha
		// per repo. The MAX(built_at) trick scopes the comparison to the
		// most recent successful build.
		rows, err := db.Query(`
			SELECT repo_path, repo_slug, commit_sha
			FROM graph_snapshots g1
			WHERE status = 'ready'
			  AND built_at = (
			      SELECT MAX(built_at) FROM graph_snapshots g2
			      WHERE g2.repo_slug = g1.repo_slug AND g2.status = 'ready'
			  )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()

		type entry struct {
			path, slug, latestSHA string
		}
		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.path, &e.slug, &e.latestSHA); err != nil {
				continue
			}
			entries = append(entries, e)
		}

		for _, e := range entries {
			if _, err := os.Stat(e.path); err != nil {
				// Repo dir gone; skip silently rather than spamming logs
				continue
			}
			if gitlocal.DirtyWorkingTree(e.path) {
				continue
			}
			head, err := gitlocal.HeadSHA(e.path)
			if err != nil || head == "" || head == e.latestSHA {
				continue
			}
			log.Printf("cmdr: graph-watch: %s: HEAD moved %s → %s, rebuilding",
				e.slug, shortSHA(e.latestSHA), shortSHA(head))
			hook(e.slug, head, e.path)
		}
		return nil
	}
}

func shortSHA(s string) string {
	if len(s) < 7 {
		return s
	}
	return s[:7]
}
