package daemon

import (
	"encoding/json"
	"net/http"

	"github.com/cmdr-tool/cmdr/internal/claude"
)

func handleAgentSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := claude.ListSessions()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}
