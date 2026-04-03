package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Pane represents a single tmux pane.
type Pane struct {
	Index   int    `json:"index"`
	CWD     string `json:"cwd"`
	Command string `json:"command"`
}

// Window represents a tmux window containing panes.
type Window struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Panes  []Pane `json:"panes"`
}

// Session represents a tmux session.
type Session struct {
	Name     string   `json:"name"`
	Attached bool     `json:"attached"`
	Windows  []Window `json:"windows"`
}

const fieldSep = "\t"
const listFormat = "#{session_name}" + fieldSep +
	"#{session_attached}" + fieldSep +
	"#{window_index}" + fieldSep +
	"#{window_name}" + fieldSep +
	"#{window_active}" + fieldSep +
	"#{pane_index}" + fieldSep +
	"#{pane_current_path}" + fieldSep +
	"#{pane_current_command}"

// socketPath returns the default tmux socket path for the current user.
// Uses /private/tmp on macOS (where /tmp is a symlink).
func socketPath() string {
	return fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())
}

// tmuxCmd creates a tmux command with an explicit socket path,
// ensuring it works when run from launchd or other non-interactive contexts.
func tmuxCmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"-S", socketPath()}, args...)
	cmd := exec.Command("tmux", fullArgs...)
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "TERM=screen")
	return cmd
}

// ListSessions returns all tmux sessions with their windows and panes.
// Returns an empty slice if tmux is not running.
func ListSessions() ([]Session, error) {
	out, err := tmuxCmd("list-panes", "-a", "-F", listFormat).Output()
	if err != nil {
		// tmux not running — not an error, just no sessions
		return []Session{}, nil
	}

	return parsePaneOutput(strings.TrimSpace(string(out))), nil
}

func parsePaneOutput(output string) []Session {
	if output == "" {
		return []Session{}
	}

	sessionMap := make(map[string]*Session)
	windowMap := make(map[string]*Window) // key: "session:winIdx"
	var sessionOrder []string

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, fieldSep)
		if len(fields) < 8 {
			continue
		}

		sessName := fields[0]
		sessAttached := fields[1] == "1"
		winIdx, _ := strconv.Atoi(fields[2])
		winName := fields[3]
		winActive := fields[4] == "1"
		paneIdx, _ := strconv.Atoi(fields[5])
		paneCWD := fields[6]
		paneCmd := fields[7]

		// Session
		sess, exists := sessionMap[sessName]
		if !exists {
			sess = &Session{Name: sessName, Attached: sessAttached}
			sessionMap[sessName] = sess
			sessionOrder = append(sessionOrder, sessName)
		}

		// Window
		winKey := sessName + ":" + strconv.Itoa(winIdx)
		win, exists := windowMap[winKey]
		if !exists {
			win = &Window{Index: winIdx, Name: winName, Active: winActive}
			windowMap[winKey] = win
			sess.Windows = append(sess.Windows, *win)
		}

		// Pane
		pane := Pane{Index: paneIdx, CWD: paneCWD, Command: paneCmd}

		// Find and update the window in the session (since we appended a copy)
		for i := range sess.Windows {
			if sess.Windows[i].Index == winIdx {
				sess.Windows[i].Panes = append(sess.Windows[i].Panes, pane)
				break
			}
		}
	}

	sessions := make([]Session, 0, len(sessionOrder))
	for _, name := range sessionOrder {
		sessions = append(sessions, *sessionMap[name])
	}
	return sessions
}
