package daemon

import (
	"database/sql"
	"log"

	"github.com/mikehu/cmdr/internal/tasks"
)

// SyncAllRepos fetches new commits for all monitored repos.
func SyncAllRepos(db *sql.DB, bus *EventBus) {
	rows, err := db.Query(`SELECT id, path, default_branch FROM repos`)
	if err != nil {
		log.Printf("cmdr: sync: query repos: %v", err)
		return
	}
	defer rows.Close()

	type repoRow struct {
		id            int
		path          string
		defaultBranch string
	}

	var repos []repoRow
	for rows.Next() {
		var r repoRow
		if err := rows.Scan(&r.id, &r.path, &r.defaultBranch); err != nil {
			continue
		}
		repos = append(repos, r)
	}

	totalNew := 0
	for _, r := range repos {
		totalNew += tasks.SyncOne(db, r.id, r.path, r.defaultBranch)
	}
	if totalNew > 0 {
		bus.Publish(Event{Type: "commits:sync", Data: true})
	}
}
