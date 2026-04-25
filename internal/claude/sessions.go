package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Session represents an active Claude Code instance.
type Session struct {
	PID            int    `json:"pid"`
	SessionID      string `json:"sessionId"`
	CWD            string `json:"cwd"`
	Project        string `json:"project"`
	StartedAt      int64  `json:"startedAt"`
	Uptime         string `json:"uptime"`
	Status     string `json:"status"` // "working", "waiting", "idle", "unknown"
	TerminalTarget string `json:"terminalTarget,omitempty"`
}

func sessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "sessions")
}

// ListSessions returns all active Claude Code sessions.
func ListSessions() ([]Session, error) {
	dir := sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Session{}, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}

		// Verify process is still alive
		if !isAlive(s.PID) {
			continue
		}

		// Derive project name from cwd
		s.Project = filepath.Base(s.CWD)

		// Calculate uptime
		if s.StartedAt > 0 {
			started := time.UnixMilli(s.StartedAt)
			s.Uptime = formatUptime(time.Since(started))
		}

		sessions = append(sessions, s)
	}

	if sessions == nil {
		sessions = []Session{}
	}
	return sessions, nil
}

func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 24 {
		days := h / 24
		h = h % 24
		if h == 0 {
			return formatPlural(days, "d")
		}
		return formatPlural(days, "d") + " " + formatPlural(h, "h")
	}
	if m == 0 {
		return formatPlural(h, "h")
	}
	return formatPlural(h, "h") + " " + formatPlural(m, "m")
}

func formatPlural(n int, unit string) string {
	return fmt.Sprintf("%d%s", n, unit)
}
