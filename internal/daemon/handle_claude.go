package daemon

import (
	"encoding/json"
	"net/http"
	"os"
)

func handleAgentSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := term.ListSessions()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		all := collectAgentInstances(sessions)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(all)
	}
}

func handleAgentKill() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			PID int `json:"pid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PID == 0 {
			http.Error(w, `{"error":"missing pid"}`, http.StatusBadRequest)
			return
		}
		// os.FindProcess always succeeds on Unix; error only surfaces at Signal.
		proc, _ := os.FindProcess(body.PID)
		if err := proc.Signal(os.Interrupt); err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"killed": body.PID})
	}
}
