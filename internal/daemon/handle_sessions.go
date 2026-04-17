package daemon

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"

)

func handleSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := term.ListSessions()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}

func handleCreateSession() http.HandlerFunc {
	type createReq struct {
		Dir string `json:"dir"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req createReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" {
			http.Error(w, `{"error":"missing dir field"}`, http.StatusBadRequest)
			return
		}

		name, err := term.CreateSession(req.Dir)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"name": name})
	}
}

func handleOpenFolder() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, `{"error":"missing path field"}`, http.StatusBadRequest)
			return
		}

		// Resolve to absolute and verify it's a directory
		abs, err := filepath.Abs(req.Path)
		if err != nil {
			http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
			return
		}

		if err := exec.Command("open", abs).Run(); err != nil {
			http.Error(w, `{"error":"failed to open folder"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"opened": abs})
	}
}
