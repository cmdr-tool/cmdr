package daemon

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/cmdr-tool/cmdr/internal/terminal"
)

func handleEditorOpen() http.HandlerFunc {
	type openReq struct {
		RepoPath string `json:"repoPath"`
		File     string `json:"file"`
		Line     int    `json:"line"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req openReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if req.RepoPath == "" || req.File == "" {
			http.Error(w, `{"error":"repoPath and file are required"}`, http.StatusBadRequest)
			return
		}
		if req.Line < 1 {
			req.Line = 1
		}

		// Find or create an nvim pane for this repo
		target, err := terminal.FindOrCreateEditor(term, req.RepoPath, req.File, req.Line)
		if err != nil {
			log.Printf("cmdr: editor/open: find editor: %v", err)
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// If nvim already existed, send :e to open the file
		if !target.Fresh {
			if err := terminal.OpenFileInEditor(term, target.Target, req.File, req.Line); err != nil {
				log.Printf("cmdr: editor/open: open file: %v", err)
				http.Error(w, jsonErr(err), http.StatusInternalServerError)
				return
			}
		}

		// Focus the terminal session
		_ = term.SwitchClient(target.Session)

		// Bring terminal to the foreground
		_ = emu.Activate()

		log.Printf("cmdr: editor/open: %s +%d %s (target %s)", req.RepoPath, req.Line, req.File, target.Target)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"target":  target.Target,
			"session": target.Session,
		})
	}
}
