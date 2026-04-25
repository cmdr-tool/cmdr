package daemon

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
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
		// Verify the target is actually an agent process before killing it.
		out, err := exec.Command("ps", "-p", strconv.Itoa(body.PID), "-o", "comm=").Output()
		if err != nil {
			http.Error(w, `{"error":"process not found"}`, http.StatusNotFound)
			return
		}
		comm := strings.TrimSpace(string(out))
		if comm != "claude" && comm != "pi" {
			http.Error(w, `{"error":"not an agent process"}`, http.StatusForbidden)
			return
		}
		// os.FindProcess always succeeds on Unix; error only surfaces at Signal.
		proc, _ := os.FindProcess(body.PID)
		if err = proc.Signal(os.Interrupt); err != nil {
			http.Error(w, jsonErr(err), http.StatusInternalServerError)
			return
		}

		// Escalate to SIGKILL if the process doesn't exit after SIGINT.
		go func() {
			time.Sleep(3 * time.Second)
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				log.Printf("cmdr: pid %d still alive after SIGINT, sending SIGKILL", body.PID)
				proc.Signal(syscall.SIGKILL)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"killed": body.PID})
	}
}
