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
			       r.name, r.path,
			       (SELECT COUNT(*) FROM review_comments rc WHERE rc.repo_path = r.path AND rc.sha = c.sha) as review_count
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

		var commits []commit
		for rows.Next() {
			var c commit
			if err := rows.Scan(&c.ID, &c.SHA, &c.Author, &c.Message, &c.CommittedAt, &c.URL,
				&c.Seen, &c.Flagged, &c.RepoName, &c.RepoPath, &c.ReviewCount); err != nil {
				continue
			}
			commits = append(commits, c)
		}
		if commits == nil {
			commits = []commit{}
		}

		// Mark commits that are already in the local branch
		markLocalCommits(commits)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(commits)
	}
}

// markLocalCommits checks which commits exist in the local branch for each repo.
// Groups commits by repo, runs one `git log` per repo, and marks matches.
func markLocalCommits(commits []commit) {
	// Group SHAs by repo path
	repoSHAs := make(map[string][]int) // repoPath → indices into commits
	for i, c := range commits {
		repoSHAs[c.RepoPath] = append(repoSHAs[c.RepoPath], i)
	}

	for repoPath, indices := range repoSHAs {
		// Get local HEAD commit SHAs (enough to cover what we show)
		out, err := exec.Command("git", "-C", repoPath, "log", "--format=%H", "-n", "100", "HEAD").Output()
		if err != nil {
			continue
		}
		localSet := make(map[string]bool)
		for _, sha := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if sha != "" {
				localSet[sha] = true
			}
		}
		for _, idx := range indices {
			commits[idx].Local = localSet[commits[idx].SHA]
		}
	}
}

// commit is the response type for the commits list API.
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
	ReviewCount int    `json:"reviewCount"`
	Local       bool   `json:"local"`
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

func handleSyncRepos(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go SyncAllRepos(db, bus)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "sync started"})
	}
}

// handleRepoPull fast-forwards the local branch to match origin.
// Checks if fast-forward is possible first, then rebases.
func handleRepoPull(bus *EventBus) http.HandlerFunc {
	type pullReq struct {
		RepoPath string `json:"repoPath"`
	}
	type pullResp struct {
		Status  string `json:"status"`  // "ok", "conflict", "error"
		Message string `json:"message"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req pullReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RepoPath == "" {
			http.Error(w, `{"error":"repoPath is required"}`, http.StatusBadRequest)
			return
		}

		// Get the default branch from the repo
		branch := detectDefaultBranch(req.RepoPath)

		// Check if fast-forward is possible: is HEAD an ancestor of origin/<branch>?
		canFF := exec.Command("git", "-C", req.RepoPath, "merge-base", "--is-ancestor", "HEAD", "origin/"+branch).Run() == nil

		w.Header().Set("Content-Type", "application/json")

		if canFF {
			// Safe fast-forward via rebase (no-op if already up to date)
			out, err := exec.Command("git", "-C", req.RepoPath, "rebase", "origin/"+branch).CombinedOutput()
			if err != nil {
				// Shouldn't happen if --is-ancestor passed, but handle it
				exec.Command("git", "-C", req.RepoPath, "rebase", "--abort").Run()
				log.Printf("cmdr: pull: %s: ff rebase failed: %s", req.RepoPath, strings.TrimSpace(string(out)))
				json.NewEncoder(w).Encode(pullResp{Status: "error", Message: strings.TrimSpace(string(out))})
				return
			}
			log.Printf("cmdr: pull: %s: fast-forwarded to origin/%s", req.RepoPath, branch)
			bus.Publish(Event{Type: "commits:sync", Data: true})
			json.NewEncoder(w).Encode(pullResp{Status: "ok", Message: fmt.Sprintf("Fast-forwarded to origin/%s", branch)})
		} else {
			// Diverged — attempt rebase
			out, err := exec.Command("git", "-C", req.RepoPath, "rebase", "origin/"+branch).CombinedOutput()
			if err != nil {
				// Rebase hit conflicts — abort and report
				exec.Command("git", "-C", req.RepoPath, "rebase", "--abort").Run()
				log.Printf("cmdr: pull: %s: rebase conflict: %s", req.RepoPath, strings.TrimSpace(string(out)))
				json.NewEncoder(w).Encode(pullResp{Status: "conflict", Message: "Rebase conflicts detected. Resolve manually or use Claude to help."})
				return
			}
			log.Printf("cmdr: pull: %s: rebased onto origin/%s", req.RepoPath, branch)
			bus.Publish(Event{Type: "commits:sync", Data: true})
			json.NewEncoder(w).Encode(pullResp{Status: "ok", Message: fmt.Sprintf("Rebased onto origin/%s", branch)})
		}
	}
}

// detectDefaultBranch returns the default branch for a repo (main or master).
func detectDefaultBranch(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// Fallback: check if main exists, else master
	if exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "origin/main").Run() == nil {
		return "main"
	}
	return "master"
}

func jsonErr(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}
