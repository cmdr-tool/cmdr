package daemon

import (
	"encoding/json"
	"net/http"
)

func handleSessionSwitch() http.HandlerFunc {
	type switchReq struct {
		Name string `json:"name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req switchReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, `{"error":"missing name field"}`, http.StatusBadRequest)
			return
		}

		if err := term.SwitchClient(req.Name); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"switched": req.Name})
	}
}

func handleSessionFocus() http.HandlerFunc {
	type focusReq struct {
		Name string `json:"name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req focusReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, `{"error":"missing name field"}`, http.StatusBadRequest)
			return
		}

		// Switch to the session
		if err := term.SwitchClient(req.Name); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		// Bring terminal to foreground (launch if not running)
		emu.Activate()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"focused": req.Name})
	}
}
