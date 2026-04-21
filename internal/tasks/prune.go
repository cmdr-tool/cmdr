package tasks

import (
	"database/sql"
	"log"
)

// Prune returns a task function that cleans up stale data: old commits,
// orphaned reviews, terminal tasks, stuck headless tasks, and inactive delegations.
func Prune(db *sql.DB) func() error {
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

		// Terminal Claude tasks older than 30 days
		res, _ = db.Exec(`DELETE FROM agent_tasks WHERE status IN ('done','failed')
			AND completed_at < datetime('now', '-30 days')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d old claude tasks", rc)
		}

		// Stale pending tasks that never launched (stuck for >1 hour).
		res, _ = db.Exec(`DELETE FROM agent_tasks WHERE status = 'pending'
			AND created_at < datetime('now', '-1 hour')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d stuck pending tasks", rc)
		}

		// Stuck headless tasks (review, ask) — these run via `claude -p` and should
		// complete in minutes. Interactive tmux sessions (directives, refactors) are
		// managed by the poller's window-alive check.
		res, _ = db.Exec(`DELETE FROM agent_tasks WHERE type IN ('review', 'ask') AND status = 'running'
			AND created_at < datetime('now', '-1 hour')`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d stuck headless tasks", rc)
		}

		// Delegation tasks for squads with no activity in 24h (delegations row cascades).
		res, _ = db.Exec(`DELETE FROM agent_tasks WHERE type = 'delegation'
			AND status IN ('completed', 'done', 'failed')
			AND id IN (
				SELECT ct.id FROM agent_tasks ct
				JOIN delegations d ON d.task_id = ct.id
				WHERE ct.type = 'delegation' AND ct.status IN ('completed', 'done', 'failed')
				AND d.squad NOT IN (
					SELECT d2.squad FROM agent_tasks ct2
					JOIN delegations d2 ON d2.task_id = ct2.id
					WHERE ct2.created_at > datetime('now', '-24 hours')
						OR ct2.status IN ('running', 'pending')
				)
			)`)
		if rc, _ := res.RowsAffected(); rc > 0 {
			log.Printf("cmdr: prune: deleted %d stale delegation tasks", rc)
		}

		return nil
	}
}
