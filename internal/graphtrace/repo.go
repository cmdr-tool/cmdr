package graphtrace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Row is a complete trace row as stored in the `traces` table. Both
// version slots are included; callers (like the JSON list endpoint)
// can null out fields they don't want to ship to the client.
type Row struct {
	ID                    int64
	RepoSlug              string
	Prompt                string
	Title                 string
	AffectedFiles         []string
	CurrentData           *Trace
	CurrentSnapshotID     *int64
	CurrentGeneratedAt    *time.Time
	CurrentStatus         string
	CurrentError          string
	PreviousData          *Trace
	PreviousSnapshotID    *int64
	PreviousGeneratedAt   *time.Time
	PreviousChangeSummary *ChangeSummary
	CreatedAt             time.Time
}

// Status values for current_status.
const (
	StatusGenerating = "generating"
	StatusReady      = "ready"
	StatusFailed     = "failed"
)

// ListByRepo returns all traces for a repo slug, ordered newest-first.
func ListByRepo(database *sql.DB, slug string) ([]Row, error) {
	rows, err := database.Query(`
		SELECT id, repo_slug, prompt, title, affected_files,
		       current_data, current_snapshot_id, current_generated_at,
		       current_status, current_error,
		       previous_data, previous_snapshot_id, previous_generated_at,
		       previous_change_summary, created_at
		FROM traces
		WHERE repo_slug = ?
		ORDER BY created_at DESC
	`, slug)
	if err != nil {
		return nil, fmt.Errorf("query traces: %w", err)
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Get fetches a single trace by id.
func Get(database *sql.DB, id int64) (Row, error) {
	row := database.QueryRow(`
		SELECT id, repo_slug, prompt, title, affected_files,
		       current_data, current_snapshot_id, current_generated_at,
		       current_status, current_error,
		       previous_data, previous_snapshot_id, previous_generated_at,
		       previous_change_summary, created_at
		FROM traces WHERE id = ?
	`, id)
	return scanRow(row)
}

// scannable is the minimal shape both *sql.Row and *sql.Rows satisfy.
type scannable interface {
	Scan(dest ...any) error
}

func scanRow(s scannable) (Row, error) {
	var r Row
	var affectedFilesJSON string
	var currentData, previousData, prevSummaryJSON sql.NullString
	var currentSnapID, previousSnapID sql.NullInt64
	var currentGeneratedAt, previousGeneratedAt sql.NullTime
	var currentError sql.NullString

	if err := s.Scan(
		&r.ID, &r.RepoSlug, &r.Prompt, &r.Title, &affectedFilesJSON,
		&currentData, &currentSnapID, &currentGeneratedAt,
		&r.CurrentStatus, &currentError,
		&previousData, &previousSnapID, &previousGeneratedAt,
		&prevSummaryJSON, &r.CreatedAt,
	); err != nil {
		return Row{}, err
	}

	if affectedFilesJSON != "" {
		_ = json.Unmarshal([]byte(affectedFilesJSON), &r.AffectedFiles)
	}
	if r.AffectedFiles == nil {
		r.AffectedFiles = []string{}
	}
	if currentData.Valid && currentData.String != "" {
		var t Trace
		if err := json.Unmarshal([]byte(currentData.String), &t); err == nil {
			r.CurrentData = &t
		}
	}
	if previousData.Valid && previousData.String != "" {
		var t Trace
		if err := json.Unmarshal([]byte(previousData.String), &t); err == nil {
			r.PreviousData = &t
		}
	}
	if prevSummaryJSON.Valid && prevSummaryJSON.String != "" {
		var cs ChangeSummary
		if err := json.Unmarshal([]byte(prevSummaryJSON.String), &cs); err == nil {
			r.PreviousChangeSummary = &cs
		}
	}
	if currentSnapID.Valid {
		v := currentSnapID.Int64
		r.CurrentSnapshotID = &v
	}
	if previousSnapID.Valid {
		v := previousSnapID.Int64
		r.PreviousSnapshotID = &v
	}
	if currentGeneratedAt.Valid {
		v := currentGeneratedAt.Time
		r.CurrentGeneratedAt = &v
	}
	if previousGeneratedAt.Valid {
		v := previousGeneratedAt.Time
		r.PreviousGeneratedAt = &v
	}
	if currentError.Valid {
		r.CurrentError = currentError.String
	}
	return r, nil
}

// Create inserts a new trace row in 'generating' state. Returns the new id.
func Create(database *sql.DB, slug, prompt, title string) (int64, error) {
	res, err := database.Exec(
		`INSERT INTO traces (repo_slug, prompt, title, current_status) VALUES (?, ?, ?, 'generating')`,
		slug, prompt, title,
	)
	if err != nil {
		return 0, fmt.Errorf("insert trace: %w", err)
	}
	return res.LastInsertId()
}

// MarkGenerating flips current_status to 'generating' and clears the prior
// error. Used at the start of a regeneration so the in-flight UI state
// reflects the active run rather than the stale terminal state from the
// previous attempt.
func MarkGenerating(database *sql.DB, id int64) error {
	_, err := database.Exec(
		`UPDATE traces SET current_status = 'generating', current_error = NULL WHERE id = ?`,
		id,
	)
	return err
}

// SetCurrentReady writes a successful generation result into the current
// slot. Caller decides whether the previous slot should have been promoted
// before this is called.
func SetCurrentReady(database *sql.DB, id int64, data Trace, snapshotID int64, affectedFiles []string) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal trace: %w", err)
	}
	if affectedFiles == nil {
		affectedFiles = []string{}
	}
	filesJSON, err := json.Marshal(affectedFiles)
	if err != nil {
		return fmt.Errorf("marshal affected_files: %w", err)
	}
	_, err = database.Exec(
		`UPDATE traces
		   SET current_data = ?, current_snapshot_id = ?, current_generated_at = ?,
		       current_status = 'ready', current_error = NULL,
		       affected_files = ?
		 WHERE id = ?`,
		string(dataJSON), snapshotID, time.Now().UTC(), string(filesJSON), id,
	)
	return err
}

// SetFailed marks a generation as failed with an error message.
func SetFailed(database *sql.DB, id int64, msg string) error {
	_, err := database.Exec(
		`UPDATE traces SET current_status = 'failed', current_error = ? WHERE id = ?`,
		msg, id,
	)
	return err
}

// PromoteCurrentToPrevious copies the row's existing current_* fields into
// previous_*, attaches the change summary, and writes the new current_*
// fields atomically. Used when regenerating against a different snapshot.
func PromoteCurrentToPrevious(database *sql.DB, id int64, newData Trace, newSnapshotID int64, newAffectedFiles []string, summary ChangeSummary) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Snapshot the current slot first so we can shift it to previous.
	var (
		curData       sql.NullString
		curSnapID     sql.NullInt64
		curGeneratedAt sql.NullTime
	)
	if err := tx.QueryRow(
		`SELECT current_data, current_snapshot_id, current_generated_at FROM traces WHERE id = ?`, id,
	).Scan(&curData, &curSnapID, &curGeneratedAt); err != nil {
		return fmt.Errorf("read current slot: %w", err)
	}

	newDataJSON, err := json.Marshal(newData)
	if err != nil {
		return err
	}
	if newAffectedFiles == nil {
		newAffectedFiles = []string{}
	}
	filesJSON, err := json.Marshal(newAffectedFiles)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		`UPDATE traces
		   SET previous_data = ?, previous_snapshot_id = ?, previous_generated_at = ?,
		       previous_change_summary = ?,
		       current_data = ?, current_snapshot_id = ?, current_generated_at = ?,
		       current_status = 'ready', current_error = NULL,
		       affected_files = ?
		 WHERE id = ?`,
		curData, curSnapID, curGeneratedAt, string(summaryJSON),
		string(newDataJSON), newSnapshotID, time.Now().UTC(),
		string(filesJSON), id,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes a trace by id.
func Delete(database *sql.DB, id int64) error {
	_, err := database.Exec(`DELETE FROM traces WHERE id = ?`, id)
	return err
}
