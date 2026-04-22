package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

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
	d, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	// Verify pragmas are active — modernc.org/sqlite silently ignores
	// malformed DSN params, so confirm WAL + busy_timeout took effect.
	var journalMode string
	d.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	var busyTimeout int
	d.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	log.Printf("cmdr: db opened (journal_mode=%s, busy_timeout=%d)", journalMode, busyTimeout)

	if err := ensureSchema(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: schema: %w", err)
	}

	if err := runMigrations(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: migrations: %w", err)
	}

	return d, nil
}

// ensureSchema creates tables and indexes if they don't exist.
// This is the canonical schema — the source of truth for what the DB should look like.
func ensureSchema(d *sql.DB) error {
	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS repos (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT NOT NULL,
			path            TEXT NOT NULL UNIQUE,
			remote_url      TEXT NOT NULL DEFAULT '',
			default_branch  TEXT NOT NULL DEFAULT 'main',
			last_synced_at  DATETIME,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			squad           TEXT NOT NULL DEFAULT '',
			squad_alias     TEXT NOT NULL DEFAULT '',
			monitor         INTEGER NOT NULL DEFAULT 1
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
			flagged       BOOLEAN NOT NULL DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repo_id, sha)
		);

		CREATE INDEX IF NOT EXISTS idx_commits_repo_date ON commits(repo_id, committed_at DESC);
		CREATE INDEX IF NOT EXISTS idx_commits_seen ON commits(seen, committed_at DESC);

		CREATE TABLE IF NOT EXISTS activity_buckets (
			slot            INTEGER NOT NULL,
			bucket          INTEGER NOT NULL,
			active_tool     TEXT NOT NULL DEFAULT '',
			claude_total     INTEGER NOT NULL DEFAULT 0,
			claude_working   INTEGER NOT NULL DEFAULT 0,
			claude_waiting   INTEGER NOT NULL DEFAULT 0,
			claude_idle      INTEGER NOT NULL DEFAULT 0,
			claude_unknown   INTEGER NOT NULL DEFAULT 0,
			pi_total         INTEGER NOT NULL DEFAULT 0,
			pi_working       INTEGER NOT NULL DEFAULT 0,
			pi_waiting       INTEGER NOT NULL DEFAULT 0,
			pi_idle          INTEGER NOT NULL DEFAULT 0,
			pi_unknown       INTEGER NOT NULL DEFAULT 0,
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

		CREATE TABLE IF NOT EXISTS agent_tasks (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			type              TEXT NOT NULL DEFAULT 'review',
			status            TEXT NOT NULL DEFAULT 'pending',
			agent             TEXT NOT NULL DEFAULT 'claude',
			repo_path         TEXT NOT NULL,
			commit_sha        TEXT NOT NULL DEFAULT '',
			prompt            TEXT NOT NULL,
			result            TEXT NOT NULL DEFAULT '',
			error_msg         TEXT NOT NULL DEFAULT '',
			title             TEXT NOT NULL DEFAULT '',
			pr_url            TEXT NOT NULL DEFAULT '',
			intent            TEXT NOT NULL DEFAULT '',
			agent_session_id  TEXT NOT NULL DEFAULT '',
			output_format     TEXT NOT NULL DEFAULT 'markdown',
			refactored        INTEGER NOT NULL DEFAULT 0,
			created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at        DATETIME,
			completed_at      DATETIME,
			worktree          TEXT NOT NULL DEFAULT '',
			parent_id         INTEGER REFERENCES agent_tasks(id),
			-- vestigial delegation columns (kept for SQLite compat, data lives in delegations table)
			squad             TEXT NOT NULL DEFAULT '',
			delegation_from   TEXT NOT NULL DEFAULT '',
			delegation_to     TEXT NOT NULL DEFAULT '',
			notified          INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS squads (
			name       TEXT PRIMARY KEY,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS delegations (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id    INTEGER NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
			squad      TEXT NOT NULL,
			from_alias TEXT NOT NULL,
			to_alias   TEXT NOT NULL,
			branch     TEXT NOT NULL DEFAULT '',
			summary    TEXT NOT NULL DEFAULT '',
			details    TEXT NOT NULL DEFAULT '',
			notified   INTEGER NOT NULL DEFAULT 0,
			UNIQUE(task_id)
		);

		CREATE TABLE IF NOT EXISTS agentic_tasks (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL UNIQUE,
			prompt      TEXT NOT NULL,
			schedule    TEXT NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 1,
			working_dir TEXT NOT NULL DEFAULT '',
			last_run_at DATETIME,
			last_result TEXT NOT NULL DEFAULT '',
			last_status TEXT NOT NULL DEFAULT '',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS migrations (
			name       TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// runMigrations executes any .sql files in migrations/ that haven't been applied yet.
// Files are sorted by name and run in order. Each runs once, tracked by the migrations table.
func runMigrations(d *sql.DB) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil || len(entries) == 0 {
		return nil
	}

	// Sort by filename for deterministic order
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		d.QueryRow(`SELECT 1 FROM migrations WHERE name = ?`, name).Scan(&applied)
		if applied {
			continue
		}

		content, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		if _, err := d.Exec(string(content)); err != nil {
			return fmt.Errorf("run %s: %w", name, err)
		}

		d.Exec(`INSERT INTO migrations (name) VALUES (?)`, name)
		log.Printf("cmdr: applied migration %s", name)
	}

	return nil
}
