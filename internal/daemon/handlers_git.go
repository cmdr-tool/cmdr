package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mikehu/cmdr/internal/gitlocal"
	"github.com/mikehu/cmdr/internal/tasks"
)

// cachedGitAuthor stores the current user's git author name, loaded once.
var cachedGitAuthor string

func gitAuthor() string {
	if cachedGitAuthor != "" {
		return cachedGitAuthor
	}
	out, err := exec.Command("git", "config", "user.name").Output()
	if err == nil {
		cachedGitAuthor = strings.TrimSpace(string(out))
	}
	return cachedGitAuthor
}

// --- Repos ---

func handleListRepos(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT id, name, path, remote_url, default_branch, last_synced_at, created_at
			FROM repos ORDER BY name
		`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type repo struct {
			ID            int     `json:"id"`
			Name          string  `json:"name"`
			Path          string  `json:"path"`
			RemoteURL     string  `json:"remoteUrl"`
			DefaultBranch string  `json:"defaultBranch"`
			LastSyncedAt  *string `json:"lastSyncedAt"`
			CreatedAt     string  `json:"createdAt"`
		}

		var repos []repo
		for rows.Next() {
			var r repo
			if err := rows.Scan(&r.ID, &r.Name, &r.Path, &r.RemoteURL, &r.DefaultBranch, &r.LastSyncedAt, &r.CreatedAt); err != nil {
				continue
			}
			repos = append(repos, r)
		}
		if repos == nil {
			repos = []repo{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(repos)
	}
}

func handleDiscoverRepos(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		codeDir := gitlocal.CodeDir()
		repos, err := gitlocal.Discover(codeDir)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Filter out already-monitored repos
		rows, err := db.Query(`SELECT path FROM repos`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		monitored := make(map[string]bool)
		for rows.Next() {
			var p string
			rows.Scan(&p)
			monitored[p] = true
		}

		var available []gitlocal.Repo
		for _, repo := range repos {
			if !monitored[repo.Path] {
				available = append(available, repo)
			}
		}
		if available == nil {
			available = []gitlocal.Repo{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(available)
	}
}

func handleAddRepo(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Path          string `json:"path"`
			Name          string `json:"name"`
			RemoteURL     string `json:"remoteUrl"`
			DefaultBranch string `json:"defaultBranch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		if body.Path == "" {
			http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
			return
		}
		if body.DefaultBranch == "" {
			body.DefaultBranch = "main"
		}

		result, err := db.Exec(`
			INSERT INTO repos (name, path, remote_url, default_branch)
			VALUES (?, ?, ?, ?)
		`, body.Name, body.Path, body.RemoteURL, body.DefaultBranch)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				http.Error(w, `{"error":"repo already monitored"}`, http.StatusConflict)
				return
			}
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		id, _ := result.LastInsertId()
		log.Printf("cmdr: git: added repo %s at %s (id=%d)", body.Name, body.Path, id)

		// Kick off initial sync in background
		go tasks.SyncOne(db, int(id), body.Path, body.DefaultBranch)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body.Name})
	}
}

func handleRemoveRepo(db *sql.DB) http.HandlerFunc {
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

		res, err := db.Exec(`DELETE FROM repos WHERE id = ?`, body.ID)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
			return
		}

		db.Exec(`DELETE FROM commits WHERE repo_id = ?`, body.ID)
		log.Printf("cmdr: git: removed repo id=%d", body.ID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"removed": body.ID})
	}
}

// --- Commits ---

func handleListCommits(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perRepo := 15
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				perRepo = n
			}
		}

		repoFilter := r.URL.Query().Get("repo")
		unseenOnly := r.URL.Query().Get("unseen") == "true"
		includeMine := r.URL.Query().Get("mine") == "true"

		// Use a windowed query to get N most recent commits per repo
		query := `
			SELECT c.id, c.sha, c.author, c.message, c.committed_at, c.url, c.seen, c.flagged,
			       r.name, r.path
			FROM commits c
			JOIN repos r ON r.id = c.repo_id
			WHERE c.id IN (
				SELECT c2.id FROM commits c2
				WHERE c2.repo_id = c.repo_id
				ORDER BY c2.committed_at DESC
				LIMIT ?
			)
		`
		args := []any{perRepo}

		// Exclude own commits by default
		if !includeMine {
			if author := gitAuthor(); author != "" {
				query += ` AND c.author != ?`
				args = append(args, author)
			}
		}

		if repoFilter != "" {
			query += ` AND (r.name = ? OR r.path = ?)`
			args = append(args, repoFilter, repoFilter)
		}
		if unseenOnly {
			query += ` AND c.seen = 0`
		}

		query += ` ORDER BY c.committed_at DESC`

		rows, err := db.Query(query, args...)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type commit struct {
			ID          int    `json:"id"`
			SHA         string `json:"sha"`
			Author      string `json:"author"`
			Message     string `json:"message"`
			CommittedAt string `json:"committedAt"`
			URL         string `json:"url"`
			Seen        bool   `json:"seen"`
			Flagged     bool   `json:"flagged"`
			RepoName    string `json:"repoName"`
			RepoPath    string `json:"repoPath"`
		}

		var commits []commit
		for rows.Next() {
			var c commit
			if err := rows.Scan(&c.ID, &c.SHA, &c.Author, &c.Message, &c.CommittedAt, &c.URL,
				&c.Seen, &c.Flagged, &c.RepoName, &c.RepoPath); err != nil {
				continue
			}
			commits = append(commits, c)
		}
		if commits == nil {
			commits = []commit{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(commits)
	}
}

func handleCommitFiles(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sha := r.URL.Query().Get("sha")
		repoPath := r.URL.Query().Get("repo")
		if sha == "" || repoPath == "" {
			http.Error(w, `{"error":"missing sha or repo parameter"}`, http.StatusBadRequest)
			return
		}

		files, err := gitlocal.CommitFiles(repoPath, sha)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		if files == nil {
			files = []gitlocal.CommitFile{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(files)
	}
}

func handleCommitDiff(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sha := r.URL.Query().Get("sha")
		repoPath := r.URL.Query().Get("repo")
		if sha == "" || repoPath == "" {
			http.Error(w, `{"error":"missing sha or repo parameter"}`, http.StatusBadRequest)
			return
		}

		result, err := gitlocal.CommitDiff(repoPath, sha)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleMarkSeen(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			IDs []int `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		if len(body.IDs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int{"marked": 0})
			return
		}

		placeholders := make([]string, len(body.IDs))
		args := make([]any, len(body.IDs))
		for i, id := range body.IDs {
			placeholders[i] = "?"
			args[i] = id
		}

		res, err := db.Exec(
			fmt.Sprintf(`UPDATE commits SET seen = 1 WHERE id IN (%s)`, strings.Join(placeholders, ",")),
			args...,
		)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"marked": n})
	}
}

func handleToggleFlag(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			ID      int  `json:"id"`
			Flagged bool `json:"flagged"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, jsonErr(err), http.StatusBadRequest)
			return
		}

		_, err := db.Exec(`UPDATE commits SET flagged = ? WHERE id = ?`, body.Flagged, body.ID)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"flagged": body.Flagged})
	}
}

func handleSyncRepos(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go SyncAllRepos(db)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "sync started"})
	}
}

func jsonErr(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}
