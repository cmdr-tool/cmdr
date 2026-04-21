package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmdr-tool/cmdr/internal/tasks"
)

type squadMember struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Alias string `json:"alias"`
}

type squad struct {
	Name      string        `json:"name"`
	CreatedAt string        `json:"createdAt"`
	Repos     []squadMember `json:"repos"`
}

func handleListSquads(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sRows, err := db.Query(`SELECT name, created_at FROM squads ORDER BY name`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer sRows.Close()

		var squads []squad
		for sRows.Next() {
			var s squad
			if err := sRows.Scan(&s.Name, &s.CreatedAt); err != nil {
				continue
			}
			s.Repos = []squadMember{}
			squads = append(squads, s)
		}
		if squads == nil {
			squads = []squad{}
		}

		// Load members for each squad
		for i, s := range squads {
			mRows, err := db.Query(
				`SELECT id, name, path, squad_alias FROM repos WHERE squad = ? ORDER BY squad_alias, name`,
				s.Name,
			)
			if err != nil {
				continue
			}
			for mRows.Next() {
				var m squadMember
				if err := mRows.Scan(&m.ID, &m.Name, &m.Path, &m.Alias); err != nil {
					continue
				}
				squads[i].Repos = append(squads[i].Repos, m)
			}
			mRows.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(squads)
	}
}

func handleCreateSquad(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}

		_, err := db.Exec(`INSERT INTO squads (name) VALUES (?)`, body.Name)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
				http.Error(w, `{"error":"squad already exists"}`, http.StatusConflict)
				return
			}
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"name": body.Name})
	}
}

func handleDeleteSquad(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}

		// Clear squad assignment on member repos
		db.Exec(`UPDATE repos SET squad='', squad_alias='' WHERE squad = ?`, body.Name)
		db.Exec(`DELETE FROM squads WHERE name = ?`, body.Name)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"deleted": body.Name})
	}
}

func handleAssignRepoSquad(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			RepoID int    `json:"repoId"`
			Squad  string `json:"squad"`
			Alias  string `json:"alias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RepoID == 0 {
			http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
			return
		}

		if body.Squad == "" {
			// Clear assignment
			db.Exec(`UPDATE repos SET squad='', squad_alias='' WHERE id = ?`, body.RepoID)
		} else {
			// Auto-derive alias from repo name if empty
			alias := body.Alias
			if alias == "" {
				var name string
				db.QueryRow(`SELECT name FROM repos WHERE id = ?`, body.RepoID).Scan(&name)
				parts := strings.Split(name, "/")
				alias = parts[len(parts)-1]
			}
			db.Exec(`UPDATE repos SET squad=?, squad_alias=? WHERE id = ?`, body.Squad, alias, body.RepoID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleUpdateRepoMonitor(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			RepoID  int  `json:"repoId"`
			Monitor bool `json:"monitor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RepoID == 0 {
			http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
			return
		}

		monVal := 0
		if body.Monitor {
			monVal = 1
		}
		db.Exec(`UPDATE repos SET monitor=? WHERE id = ?`, monVal, body.RepoID)

		// Sync commits when monitoring is turned on
		if body.Monitor {
			var path, branch string
			if err := db.QueryRow(`SELECT path, default_branch FROM repos WHERE id = ?`, body.RepoID).Scan(&path, &branch); err == nil {
				log.Printf("cmdr: monitor enabled for repo %d, syncing", body.RepoID)
				go func() {
					if n := tasks.SyncOne(db, body.RepoID, path, branch); n > 0 {
						bus.Publish(Event{Type: "commits:sync", Data: true})
					}
				}()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleListDelegations(db *sql.DB) http.HandlerFunc {
	type delegation struct {
		ID             int    `json:"id"`
		Status         string `json:"status"`
		Squad          string `json:"squad"`
		DelegationFrom string `json:"delegationFrom"`
		DelegationTo   string `json:"delegationTo"`
		Title          string `json:"title"`
		Summary        string `json:"summary"`
		Branch         string `json:"branch"`
		RepoPath       string `json:"repoPath"`
		Result         string `json:"result,omitempty"`
		CreatedAt      string `json:"createdAt"`
		CompletedAt    string `json:"completedAt,omitempty"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		squadFilter := r.URL.Query().Get("squad")

		query := `SELECT ct.id, ct.status, d.squad, d.from_alias, d.to_alias,
				COALESCE(ct.title, ''), d.summary, d.branch, ct.repo_path,
				COALESCE(ct.result, ''), ct.created_at, COALESCE(ct.completed_at, '')
			FROM agent_tasks ct
			JOIN delegations d ON d.task_id = ct.id
			WHERE ct.type = 'delegation'`
		var args []any
		if squadFilter != "" {
			query += ` AND d.squad = ?`
			args = append(args, squadFilter)
		}
		query += ` ORDER BY ct.created_at DESC`

		rows, err := db.Query(query, args...)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var delegations []delegation
		for rows.Next() {
			var d delegation
			if err := rows.Scan(&d.ID, &d.Status, &d.Squad, &d.DelegationFrom, &d.DelegationTo, &d.Title, &d.Summary, &d.Branch, &d.RepoPath, &d.Result, &d.CreatedAt, &d.CompletedAt); err != nil {
				continue
			}
			delegations = append(delegations, d)
		}
		if delegations == nil {
			delegations = []delegation{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(delegations)
	}
}

func handleDelegationSummary(db *sql.DB) http.HandlerFunc {
	type summary struct {
		Squad       string   `json:"squad"`
		ActiveCount int      `json:"activeCount"`
		TotalCount  int      `json:"totalCount"`
		Members     []string `json:"members"`
		LatestAt    string   `json:"latestAt"`
		LatestTitle string   `json:"latestTitle"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT d.squad,
				SUM(CASE WHEN ct.status IN ('running','pending') THEN 1 ELSE 0 END),
				COUNT(*),
				MAX(ct.created_at)
			FROM agent_tasks ct
			JOIN delegations d ON d.task_id = ct.id
			WHERE ct.type = 'delegation'
			GROUP BY d.squad
			HAVING COUNT(*) > 0
		`)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var summaries []summary
		for rows.Next() {
			var s summary
			if err := rows.Scan(&s.Squad, &s.ActiveCount, &s.TotalCount, &s.LatestAt); err != nil {
				continue
			}

			// Get unique member aliases
			mRows, _ := db.Query(
				`SELECT DISTINCT from_alias FROM delegations WHERE squad = ?
				 UNION SELECT DISTINCT to_alias FROM delegations WHERE squad = ?`,
				s.Squad, s.Squad,
			)
			if mRows != nil {
				for mRows.Next() {
					var alias string
					mRows.Scan(&alias)
					s.Members = append(s.Members, alias)
				}
				mRows.Close()
			}
			if s.Members == nil {
				s.Members = []string{}
			}

			// Get latest title
			db.QueryRow(
				`SELECT COALESCE(ct.title, d.summary) FROM agent_tasks ct
				 JOIN delegations d ON d.task_id = ct.id
				 WHERE d.squad = ? ORDER BY ct.created_at DESC LIMIT 1`, s.Squad,
			).Scan(&s.LatestTitle)

			summaries = append(summaries, s)
		}
		if summaries == nil {
			summaries = []summary{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(summaries)
	}
}

func handleEnlist(db *sql.DB, bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Squad   string `json:"squad"`
			From    string `json:"from"`
			To      string `json:"to"`
			Summary string `json:"summary"`
			Details string `json:"details"`
			PR      bool   `json:"pr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if body.Squad == "" || body.From == "" || body.To == "" || body.Summary == "" {
			http.Error(w, `{"error":"squad, from, to, and summary are required"}`, http.StatusBadRequest)
			return
		}

		// Validate squad exists
		var squadExists bool
		db.QueryRow(`SELECT COUNT(*) FROM squads WHERE name = ?`, body.Squad).Scan(&squadExists)
		if !squadExists {
			http.Error(w, fmt.Sprintf(`{"error":"squad %q does not exist"}`, body.Squad), http.StatusNotFound)
			return
		}

		// Resolve target alias to repo path
		var targetPath string
		if err := db.QueryRow(`SELECT path FROM repos WHERE squad = ? AND squad_alias = ?`, body.Squad, body.To).Scan(&targetPath); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"squad member %q not found in squad %q"}`, body.To, body.Squad), http.StatusNotFound)
			return
		}

		// Check no running delegation already targets this repo
		var running int
		db.QueryRow(`SELECT COUNT(*) FROM agent_tasks WHERE type = 'delegation' AND repo_path = ? AND status = 'running'`, targetPath).Scan(&running)
		if running > 0 {
			http.Error(w, fmt.Sprintf(`{"error":"%s already has an active delegation"}`, body.To), http.StatusConflict)
			return
		}

		// Build delivery instructions based on --pr flag
		var delivery string
		if body.PR {
			delivery = "## Delivery\n\nWhen complete, commit your changes to the branch and push. Then open a pull request with a clear title and description summarizing what you changed and why."
		} else {
			delivery = "## Delivery\n\nWhen complete, commit your changes with a clear message, merge your branch into main, and push."
		}

		// Create task row
		prompt := fmt.Sprintf("## Enlistment from %s\n\n**Summary:** %s\n\n%s\n\n%s", body.From, body.Summary, body.Details, delivery)
		now := time.Now().Format(time.RFC3339)
		taskResult, err := db.Exec(
			`INSERT INTO agent_tasks (type, status, repo_path, prompt, intent, created_at, started_at)
			 VALUES ('delegation', 'pending', ?, ?, 'delegation', ?, ?)`,
			targetPath, prompt, now, now,
		)
		if err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		taskID64, _ := taskResult.LastInsertId()
		taskID := int(taskID64)

		// Insert delegation details
		branchName := fmt.Sprintf("squad/%s/%d", body.Squad, taskID)
		if _, err := db.Exec(
			`INSERT INTO delegations (task_id, squad, from_alias, to_alias, branch, summary, details)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			taskID, body.Squad, body.From, body.To, branchName, body.Summary, body.Details,
		); err != nil {
			db.Exec(`DELETE FROM agent_tasks WHERE id = ?`, taskID)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Debrief path in /tmp — transient, captured by poller then deleted
		debriefDir := filepath.Join(os.TempDir(), "cmdr")
		os.MkdirAll(debriefDir, 0o700)
		debriefPath := filepath.Join(debriefDir, fmt.Sprintf("debrief-%d.md", taskID))
		prompt += fmt.Sprintf("\n\n---\n\nDEBRIEF_PATH: %s", debriefPath)

		// Launch via unified launchTask — gets worktree, tmux session, system prompt
		res, err := launchTask(db, bus, TaskLaunchConfig{
			TaskID:         taskID,
			Intent:         "delegation",
			UserPrompt:     prompt,
			RepoPath:       targetPath,
			WindowPrefix:   "enlist",
			WorktreePrefix: fmt.Sprintf("enlist-%s", body.Squad),
		})
		if err != nil {
			db.Exec(`DELETE FROM delegations WHERE task_id = ?`, taskID)
			db.Exec(`DELETE FROM agent_tasks WHERE id = ?`, taskID)
			log.Printf("cmdr: enlist launch failed: %v", err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Notify frontend
		bus.Publish(Event{Type: "delegation:update", Data: map[string]any{
			"squad": body.Squad, "taskId": taskID, "status": "running",
		}})

		log.Printf("cmdr: enlistment dispatched (task %d, squad %s, %s → %s)", taskID, body.Squad, body.From, body.To)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"taskId":  taskID,
			"branch":  branchName,
			"session": res.Session,
			"window":  res.Window,
		})
	}
}
