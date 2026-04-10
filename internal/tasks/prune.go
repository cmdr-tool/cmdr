package tasks

import (
	"database/sql"
	"log"
)

// PruneCommits returns a task function that deletes commits older than 2 weeks
// and cleans up orphaned review data and stale Claude tasks.
func PruneCommits(db *sql.DB) func() error {
	return func() error {
		result, err := db.Exec(`DELETE FROM commits WHERE committed_at < datetime('now', '-14 days')`)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			log.Printf("cmdr: prune: deleted %d old commits", n)
		}

		// Orphaned review comments (commit no longer in DB)
		res, _ := db.Exec(`DELETE FROM review_comments WHERE NOT EXISTS (
			SELECT 1 FROM commits c JOIN repos r ON r.id = c.repo_id
			WHERE r.path = review_comments.repo_path AND c.sha = review_comments.sha
		)`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d orphaned review comments", rc)
		}

		// Completed/failed Claude tasks older than 30 days
		res, _ = db.Exec(`DELETE FROM claude_tasks WHERE status IN ('completed','failed')
			AND completed_at < datetime('now', '-30 days')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d old claude tasks", rc)
		}

		// Stale pending tasks that never launched (stuck for >1 hour).
		res, _ = db.Exec(`DELETE FROM claude_tasks WHERE status = 'pending'
			AND created_at < datetime('now', '-1 hour')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d stuck pending tasks", rc)
		}

		// Stuck headless review tasks (type='review', status='running') — these run
		// via `claude -p` and should complete in minutes. Interactive tmux sessions
		// (directives, refactors) are managed by the poller's window-alive check.
		res, _ = db.Exec(`DELETE FROM claude_tasks WHERE type = 'review' AND status = 'running'
			AND created_at < datetime('now', '-1 hour')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d stuck headless review tasks", rc)
		}

		return nil
	}
}
