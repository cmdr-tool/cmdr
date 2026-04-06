package tasks

import (
	"database/sql"
	"log"
	"time"

	"github.com/mikehu/cmdr/internal/gitlocal"
)

// SyncCommits returns a task function that fetches new commits for all monitored repos.
// If onNew is non-nil, it is called once after the sync when any new commits were inserted.
func SyncCommits(db *sql.DB, onNew func()) func() error {
	return func() error {
		rows, err := db.Query(`SELECT id, path, default_branch FROM repos`)
		if err != nil {
			return err
		}
		defer rows.Close()

		type repo struct {
			id            int
			path          string
			defaultBranch string
		}

		var repos []repo
		for rows.Next() {
			var r repo
			if err := rows.Scan(&r.id, &r.path, &r.defaultBranch); err != nil {
				continue
			}
			repos = append(repos, r)
		}

		totalNew := 0
		for _, r := range repos {
			totalNew += SyncOne(db, r.id, r.path, r.defaultBranch)
		}
		if totalNew > 0 && onNew != nil {
			onNew()
		}
		return nil
	}
}

// SyncOne fetches and stores new commits for a single repo.
// Returns the number of newly inserted commits.
func SyncOne(db *sql.DB, repoID int, repoPath, defaultBranch string) int {
	// Fetch latest from remote
	if err := gitlocal.Fetch(repoPath); err != nil {
		log.Printf("cmdr: sync: %s: fetch failed: %v", repoPath, err)
		return 0
	}

	commits, err := gitlocal.Log(repoPath, defaultBranch, 50)
	if err != nil {
		log.Printf("cmdr: sync: %s: log failed: %v", repoPath, err)
		return 0
	}

	inserted := 0
	for _, c := range commits {
		res, err := db.Exec(`
			INSERT OR IGNORE INTO commits (repo_id, sha, author, message, committed_at, url)
			VALUES (?, ?, ?, ?, ?, ?)
		`, repoID, c.SHA, c.Author, c.Message, c.CommittedAt.Format(time.RFC3339), c.URL)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				inserted++
			}
		}
	}

	db.Exec(`UPDATE repos SET last_synced_at = ? WHERE id = ?`, time.Now().Format(time.RFC3339), repoID)

	if inserted > 0 {
		log.Printf("cmdr: sync: %s: %d new commits", repoPath, inserted)
	}
	return inserted
}
