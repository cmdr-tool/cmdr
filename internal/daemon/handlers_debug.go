package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/cmdr-tool/cmdr/internal/gitlocal"
)

func registerDebugAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/_debug/env", handleDebugEnv())
	mux.HandleFunc("/api/_debug/tmux", handleDebugTmux())
	mux.HandleFunc("/api/_debug/codedir", handleDebugCodeDir())
	mux.HandleFunc("/api/_debug/sessions", handleDebugSessions())
}

func handleDebugEnv() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		env := map[string]string{
			"PATH":          os.Getenv("PATH"),
			"HOME":          os.Getenv("HOME"),
			"TMPDIR":        os.Getenv("TMPDIR"),
			"USER":          os.Getenv("USER"),
			"CMDR_CODE_DIR": os.Getenv("CMDR_CODE_DIR"),
			"UID":           fmt.Sprintf("%d", os.Getuid()),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(env)
	}
}

func handleDebugTmux() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		socketPath := fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())

		// Check socket exists
		_, statErr := os.Stat(socketPath)

		// Run tmux command and capture both stdout and stderr
		cmd := exec.Command("tmux", "-S", socketPath, "list-panes", "-a", "-F",
			"#{session_name}\t#{pane_current_command}")
		stdout, err := cmd.CombinedOutput()

		// Also try without explicit socket
		cmd2 := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}")
		stdout2, err2 := cmd2.CombinedOutput()

		result := map[string]any{
			"socketPath":        socketPath,
			"socketExists":      statErr == nil,
			"socketStatError":   fmt.Sprintf("%v", statErr),
			"withSocket":        strings.TrimSpace(string(stdout)),
			"withSocketError":   fmt.Sprintf("%v", err),
			"withoutSocket":     strings.TrimSpace(string(stdout2)),
			"withoutSocketError": fmt.Sprintf("%v", err2),
			"tmuxPath":          tmuxPath(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleDebugCodeDir() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		codeDir := gitlocal.CodeDir()
		entries, err := os.ReadDir(codeDir)

		var dirs []string
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, e.Name())
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"codeDir": codeDir,
			"error":   fmt.Sprintf("%v", err),
			"dirs":    dirs,
		})
	}
}

func handleDebugSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := term.ListSessions()

		// Also run the raw command to compare
		socketPath := fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())
		format := "#{session_name}\t#{session_attached}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_index}\t#{pane_current_path}\t#{pane_current_command}"
		cmd := exec.Command("tmux", "-S", socketPath, "list-panes", "-a", "-F", format)
		rawOut, rawErr := cmd.CombinedOutput()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sessions":  sessions,
			"count":     len(sessions),
			"error":     fmt.Sprintf("%v", err),
			"rawOutput": string(rawOut),
			"rawError":  fmt.Sprintf("%v", rawErr),
			"rawLen":    len(rawOut),
		})
	}
}

func tmuxPath() string {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return "not found"
	}
	return path
}
