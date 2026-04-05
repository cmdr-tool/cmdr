package daemon

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mikehu/cmdr/internal/gitlocal"
	"github.com/mikehu/cmdr/internal/prompts"
	"github.com/mikehu/cmdr/internal/tasks"
)

// --- Review Comments ---

type reviewComment struct {
	ID        int    `json:"id"`
	RepoPath  string `json:"repoPath"`
	SHA       string `json:"sha"`
	LineStart int    `json:"lineStart"`
	LineEnd   int    `json:"lineEnd"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"createdAt"`
}

func handleListReviewComments(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo := r.URL.Query().Get("repo")
		sha := r.URL.Query().Get("sha")
		if repo == "" || sha == "" {
			http.Error(w, `{"error":"missing repo or sha"}`, http.StatusBadRequest)
			return
		}

		rows, err := db.Query(`
			SELECT id, repo_path, sha, line_start, line_end, comment, created_at
			FROM review_comments WHERE repo_path = ? AND sha = ?
			ORDER BY line_start
		`, repo, sha)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var comments []reviewComment
		for rows.Next() {
			var c reviewComment
			if err := rows.Scan(&c.ID, &c.RepoPath, &c.SHA, &c.LineStart, &c.LineEnd, &c.Comment, &c.CreatedAt); err != nil {
				continue
			}
			comments = append(comments, c)
		}
		if comments == nil {
			comments = []reviewComment{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(comments)
	}
}

func handleSaveReviewComment(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			RepoPath  string `json:"repoPath"`
			SHA       string `json:"sha"`
			LineStart int    `json:"lineStart"`
			LineEnd   int    `json:"lineEnd"`
			Comment   string `json:"comment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		res, err := db.Exec(`
			INSERT INTO review_comments (repo_path, sha, line_start, line_end, comment)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(repo_path, sha, line_start, line_end) DO UPDATE SET comment = excluded.comment
		`, body.RepoPath, body.SHA, body.LineStart, body.LineEnd, body.Comment)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		id, _ := res.LastInsertId()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"id": id})
	}
}

func handleDeleteReviewComment(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID int `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		db.Exec(`DELETE FROM review_comments WHERE id = ?`, body.ID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// --- Review Submission ---

func handleSubmitReview(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			RepoPath string `json:"repoPath"`
			SHA      string `json:"sha"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		// Load review comments
		rows, err := db.Query(`
			SELECT line_start, line_end, comment
			FROM review_comments WHERE repo_path = ? AND sha = ?
			ORDER BY line_start
		`, body.RepoPath, body.SHA)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var annotations []reviewAnnotation
		for rows.Next() {
			var a reviewAnnotation
			if err := rows.Scan(&a.lineStart, &a.lineEnd, &a.comment); err != nil {
				continue
			}
			annotations = append(annotations, a)
		}

		// Load commit metadata
		var author, message, committedAt, repoName string
		db.QueryRow(`
			SELECT c.author, c.message, c.committed_at, r.name
			FROM commits c JOIN repos r ON r.id = c.repo_id
			WHERE r.path = ? AND c.sha = ?
		`, body.RepoPath, body.SHA).Scan(&author, &message, &committedAt, &repoName)

		// Load diff (plain text for prompt)
		diffResult, err := gitlocal.CommitDiff(body.RepoPath, body.SHA)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		diffText := diffResult.Diff
		if diffResult.Format == "delta" {
			diffText = stripHTML(diffText)
		}

		// Build prompt from template
		diffLines := strings.Split(diffText, "\n")
		var promptAnnotations []prompts.ReviewAnnotation
		for _, a := range annotations {
			var ctx strings.Builder
			for i := a.lineStart - 1; i < a.lineEnd && i < len(diffLines); i++ {
				if i >= 0 {
					ctx.WriteString(diffLines[i])
					ctx.WriteByte('\n')
				}
			}
			promptAnnotations = append(promptAnnotations, prompts.ReviewAnnotation{
				LineStart: a.lineStart,
				LineEnd:   a.lineEnd,
				Context:   strings.TrimRight(ctx.String(), "\n"),
				Comment:   a.comment,
			})
		}

		prompt, err := prompts.Review(prompts.ReviewData{
			RepoName:    repoName,
			SHA:         body.SHA,
			Author:      author,
			Date:        committedAt,
			Message:     message,
			Diff:        diffText,
			Annotations: promptAnnotations,
		})
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Create task
		res, err := db.Exec(`
			INSERT INTO claude_tasks (type, status, repo_path, commit_sha, prompt, created_at)
			VALUES ('review', 'pending', ?, ?, ?, ?)
		`, body.RepoPath, body.SHA, prompt, time.Now().Format(time.RFC3339))
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		taskID, _ := res.LastInsertId()

		// Launch async
		go runClaudeReview(db, bus, int(taskID), body.RepoPath, body.SHA, prompt)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": taskID, "status": "pending"})
	}
}

func runClaudeReview(db *sql.DB, bus *EventBus, taskID int, repoPath, sha, prompt string) {
	db.Exec(`UPDATE claude_tasks SET status='running', started_at=? WHERE id=?`,
		time.Now().Format(time.RFC3339), taskID)
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": taskID, "status": "running", "repoPath": repoPath, "commitSha": sha,
	}})

	log.Printf("cmdr: claude review started (task %d, %s %s)", taskID, repoPath, sha[:7])

	result, err := tasks.Claude(prompt, repoPath)

	now := time.Now().Format(time.RFC3339)
	if err != nil {
		db.Exec(`UPDATE claude_tasks SET status='failed', error_msg=?, completed_at=? WHERE id=?`,
			err.Error(), now, taskID)
		bus.Publish(Event{Type: "claude:task", Data: map[string]any{
			"id": taskID, "status": "failed",
		}})
		log.Printf("cmdr: claude review failed (task %d): %v", taskID, err)
		return
	}

	db.Exec(`UPDATE claude_tasks SET status='completed', result=?, completed_at=? WHERE id=?`,
		result, now, taskID)
	bus.Publish(Event{Type: "claude:task", Data: map[string]any{
		"id": taskID, "status": "completed",
	}})

	// Clean up review comments — they've been consumed
	db.Exec(`DELETE FROM review_comments WHERE repo_path=? AND sha=?`, repoPath, sha)

	log.Printf("cmdr: claude review completed (task %d)", taskID)
}

type reviewAnnotation struct {
	lineStart, lineEnd int
	comment            string
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, "")
}

// --- Claude Tasks ---

func handleListClaudeTasks(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := `SELECT id, type, status, repo_path, commit_sha, error_msg, created_at, started_at, completed_at
			FROM claude_tasks ORDER BY created_at DESC LIMIT 50`
		rows, err := db.Query(query)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type task struct {
			ID          int     `json:"id"`
			Type        string  `json:"type"`
			Status      string  `json:"status"`
			RepoPath    string  `json:"repoPath"`
			CommitSHA   string  `json:"commitSha"`
			ErrorMsg    string  `json:"errorMsg,omitempty"`
			CreatedAt   string  `json:"createdAt"`
			StartedAt   *string `json:"startedAt"`
			CompletedAt *string `json:"completedAt"`
		}

		var taskList []task
		for rows.Next() {
			var t task
			if err := rows.Scan(&t.ID, &t.Type, &t.Status, &t.RepoPath, &t.CommitSHA,
				&t.ErrorMsg, &t.CreatedAt, &t.StartedAt, &t.CompletedAt); err != nil {
				continue
			}
			taskList = append(taskList, t)
		}
		if taskList == nil {
			taskList = []task{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(taskList)
	}
}

func handleGetClaudeTaskResult(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		var result, status, errMsg string
		err := db.QueryRow(`SELECT result, status, error_msg FROM claude_tasks WHERE id = ?`, id).
			Scan(&result, &status, &errMsg)
		if err != nil {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"result":   result,
			"status":   status,
			"errorMsg": errMsg,
		})
	}
}

func handleDismissClaudeTask(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID  int    `json:"id"`
			All string `json:"all"` // "completed" to clear all completed
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		var res sql.Result
		var err error
		if body.All == "completed" {
			res, err = db.Exec(`DELETE FROM claude_tasks WHERE status IN ('completed', 'failed')`)
		} else if body.ID > 0 {
			res, err = db.Exec(`DELETE FROM claude_tasks WHERE id = ?`, body.ID)
		} else {
			http.Error(w, `{"error":"missing id or all"}`, http.StatusBadRequest)
			return
		}

		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"dismissed": n})
	}
}
