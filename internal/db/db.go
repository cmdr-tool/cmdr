package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open creates or opens the cmdr SQLite database at ~/.cmdr/cmdr.db.
func Open() (*sql.DB, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("db: user home: %w", err)
	}

	dir := filepath.Join(home, ".cmdr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("db: mkdir: %w", err)
	}

	path := filepath.Join(dir, "cmdr.db")
	d, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	if err := migrate(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return d, nil
}

func migrate(d *sql.DB) error {
	// Check if we need to migrate from old schema (has 'owner' column)
	var hasOwner bool
	row := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('repos') WHERE name='owner'`)
	row.Scan(&hasOwner)
	if hasOwner {
		// Drop old tables and recreate
		d.Exec(`DROP TABLE IF EXISTS commits`)
		d.Exec(`DROP TABLE IF EXISTS repos`)
	}

	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS repos (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT NOT NULL,
			path            TEXT NOT NULL UNIQUE,
			remote_url      TEXT NOT NULL DEFAULT '',
			default_branch  TEXT NOT NULL DEFAULT 'main',
			last_synced_at  DATETIME,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS commits (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
			sha           TEXT NOT NULL,
			author        TEXT NOT NULL,
			message       TEXT NOT NULL,
			committed_at  DATETIME NOT NULL,
			url           TEXT NOT NULL DEFAULT '',
			seen          BOOLEAN NOT NULL DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repo_id, sha)
		);

		CREATE INDEX IF NOT EXISTS idx_commits_repo_date ON commits(repo_id, committed_at DESC);
		CREATE INDEX IF NOT EXISTS idx_commits_seen ON commits(seen, committed_at DESC);
	`)
	if err != nil {
		return err
	}

	// Add flagged column if missing
	var hasFlagged bool
	d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('commits') WHERE name='flagged'`).Scan(&hasFlagged)
	if !hasFlagged {
		d.Exec(`ALTER TABLE commits ADD COLUMN flagged BOOLEAN NOT NULL DEFAULT 0`)
	}

	_, err = d.Exec(`
		CREATE TABLE IF NOT EXISTS activity_buckets (
			slot            INTEGER NOT NULL,
			bucket          INTEGER NOT NULL,
			active_tool     TEXT NOT NULL DEFAULT '',
			claude_total    INTEGER NOT NULL DEFAULT 0,
			claude_working  INTEGER NOT NULL DEFAULT 0,
			claude_waiting  INTEGER NOT NULL DEFAULT 0,
			claude_idle     INTEGER NOT NULL DEFAULT 0,
			claude_unknown  INTEGER NOT NULL DEFAULT 0,
			recorded_at     DATETIME,
			PRIMARY KEY (slot, bucket)
		);

		CREATE TABLE IF NOT EXISTS review_comments (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_path   TEXT NOT NULL,
			sha         TEXT NOT NULL,
			line_start  INTEGER NOT NULL,
			line_end    INTEGER NOT NULL,
			comment     TEXT NOT NULL,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repo_path, sha, line_start, line_end)
		);

		CREATE TABLE IF NOT EXISTS claude_tasks (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			type          TEXT NOT NULL DEFAULT 'review',
			status        TEXT NOT NULL DEFAULT 'pending',
			repo_path     TEXT NOT NULL,
			commit_sha    TEXT NOT NULL DEFAULT '',
			prompt        TEXT NOT NULL,
			result        TEXT NOT NULL DEFAULT '',
			error_msg     TEXT NOT NULL DEFAULT '',
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at    DATETIME,
			completed_at  DATETIME
		);
	`)
	if err != nil {
		return err
	}

	// Add title column to claude_tasks if missing
	var hasTitle bool
	d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('claude_tasks') WHERE name='title'`).Scan(&hasTitle)
	if !hasTitle {
		d.Exec(`ALTER TABLE claude_tasks ADD COLUMN title TEXT NOT NULL DEFAULT ''`)
	}

	// Add pr_url column to claude_tasks if missing
	var hasPrUrl bool
	d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('claude_tasks') WHERE name='pr_url'`).Scan(&hasPrUrl)
	if !hasPrUrl {
		d.Exec(`ALTER TABLE claude_tasks ADD COLUMN pr_url TEXT NOT NULL DEFAULT ''`)
	}

	// Add refactored flag to claude_tasks if missing
	var hasRefactored bool
	d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('claude_tasks') WHERE name='refactored'`).Scan(&hasRefactored)
	if !hasRefactored {
		d.Exec(`ALTER TABLE claude_tasks ADD COLUMN refactored INTEGER NOT NULL DEFAULT 0`)
	}

	// Add intent column to claude_tasks if missing
	var hasIntent bool
	d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('claude_tasks') WHERE name='intent'`).Scan(&hasIntent)
	if !hasIntent {
		d.Exec(`ALTER TABLE claude_tasks ADD COLUMN intent TEXT NOT NULL DEFAULT ''`)
	}

	// Clear stale titles on directives (now derived on read)
	d.Exec(`UPDATE claude_tasks SET title='' WHERE type='directive'`)

	// Clean up unused drafts table if it exists
	d.Exec(`DROP TABLE IF EXISTS drafts`)

	return nil
}
