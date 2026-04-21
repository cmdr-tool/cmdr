package daemon

import (
	"encoding/json"
	"net/http"

	"github.com/cmdr-tool/cmdr/internal/agent"
)

func handleAgentSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var all []agent.Instance
		for _, a := range agent.All() {
			instances, err := a.DetectInstances()
			if err != nil {
				continue
			}
			all = append(all, instances...)
		}
		if all == nil {
			all = []agent.Instance{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(all)
	}
}
