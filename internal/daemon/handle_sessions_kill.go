package daemon

import (
	"encoding/json"
	"net/http"

)

func handleSessionKill() http.HandlerFunc {
	type killReq struct {
		Name string `json:"name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req killReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, `{"error":"missing name field"}`, http.StatusBadRequest)
			return
		}

		if err := term.KillSession(req.Name); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"killed": req.Name})
	}
}
