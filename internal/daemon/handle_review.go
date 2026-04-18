package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/gitlocal"
	"github.com/cmdr-tool/cmdr/internal/prompts"
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

func handleSaveReviewComment(db *sql.DB, bus *EventBus) http.HandlerFunc {
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
		bus.Publish(Event{Type: "commits:sync", Data: true})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"id": id})
	}
}

func handleDeleteReviewComment(db *sql.DB, bus *EventBus) http.HandlerFunc {
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
		bus.Publish(Event{Type: "commits:sync", Data: true})
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
		var commitNote string
		for rows.Next() {
			var a reviewAnnotation
			if err := rows.Scan(&a.lineStart, &a.lineEnd, &a.comment); err != nil {
				continue
			}
			if a.lineStart == 0 && a.lineEnd == 0 {
				commitNote = a.comment
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
			CommitNote:  commitNote,
		})
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Create task with a descriptive default title
		shortSHA := body.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		title := fmt.Sprintf("Review %s: %s", shortSHA, firstLine(message))
		res, err := db.Exec(`
			INSERT INTO claude_tasks (type, status, repo_path, commit_sha, prompt, title, created_at)
			VALUES ('review', 'pending', ?, ?, ?, ?, ?)
		`, body.RepoPath, body.SHA, prompt, title, time.Now().Format(time.RFC3339))
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		taskID, _ := res.LastInsertId()

		// Launch async — title is enhanced on completion via runHeadless
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

	// Use the headless runner for streaming, process tracking, and cancellation
	runHeadless(db, bus, HeadlessConfig{
		TaskID:  taskID,
		Prompt:  prompt,
		WorkDir: repoPath,
	})

	// Clean up review comments after completion (only if task succeeded)
	var status string
	db.QueryRow(`SELECT status FROM claude_tasks WHERE id=?`, taskID).Scan(&status)
	if status == "resolved" {
		db.Exec(`DELETE FROM review_comments WHERE repo_path=? AND sha=?`, repoPath, sha)
		bus.Publish(Event{Type: "commits:sync", Data: true})
	}
}

type reviewAnnotation struct {
	lineStart, lineEnd int
	comment            string
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
