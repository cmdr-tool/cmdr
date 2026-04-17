// Package tmux implements the terminal.Multiplexer interface using tmux.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cmdr-tool/cmdr/internal/terminal"
)

func init() {
	terminal.Register("tmux", func() terminal.Multiplexer {
		return &Adapter{}
	})
}

// Adapter implements terminal.Multiplexer via tmux commands.
type Adapter struct{}

// socketPath returns the default tmux socket path for the current user.
func socketPath() string {
	return fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())
}

// cmd creates a tmux command with an explicit socket path,
// ensuring it works from launchd or other non-interactive contexts.
func cmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"-S", socketPath()}, args...)
	c := exec.Command("tmux", fullArgs...)
	c.Env = append(os.Environ(), "LANG=en_US.UTF-8", "TERM=screen")
	return c
}

func (a *Adapter) ListSessions() ([]terminal.Session, error) {
	out, err := cmd("list-panes", "-a", "-F", listFormat).Output()
	if err != nil {
		return []terminal.Session{}, nil
	}
	return parsePaneOutput(strings.TrimSpace(string(out))), nil
}

func (a *Adapter) CreateSession(dir string) (string, error) {
	name := terminal.SessionName(dir)
	if err := cmd("has-session", "-t="+name).Run(); err == nil {
		return name, nil
	}
	if out, err := cmd("new-session", "-ds", name, "-c", dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return name, nil
}

func (a *Adapter) KillSession(name string) error {
	out, err := cmd("kill-session", "-t", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (a *Adapter) SwitchClient(name string) error {
	out, err := cmd("switch-client", "-t", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux switch-client: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (a *Adapter) CreateWindow(session, windowName, dir, shellCmd string) (string, error) {
	cmdArgs := []string{"new-window", "-t", session, "-n", windowName, "-c", dir, "bash", "-c", shellCmd}
	if out, err := cmd(cmdArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux new-window: %s: %w", strings.TrimSpace(string(out)), err)
	}
	_ = a.SwitchClient(session)
	return session + ":" + windowName, nil
}

func (a *Adapter) KillWindow(target string) error {
	out, err := cmd("kill-window", "-t", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-window: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (a *Adapter) SendKeys(target, keys string, literal bool) error {
	args := []string{"send-keys", "-t", target}
	if literal {
		args = append(args, keys, "Enter")
	} else {
		args = append(args, strings.Fields(keys)...)
	}
	out, err := cmd(args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (a *Adapter) CapturePane(target string, lines int) (string, error) {
	lineArg := fmt.Sprintf("-%d", lines)
	out, err := cmd("capture-pane", "-t", target, "-p", "-S", lineArg).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

func (a *Adapter) WindowExists(target string) bool {
	return cmd("list-panes", "-t", target).Run() == nil
}

func (a *Adapter) OpenInEditor(dir, file string, line int) (*terminal.EditorTarget, error) {
	sessions, err := a.ListSessions()
	if err != nil {
		return nil, err
	}

	dir = terminal.ResolveRepoPath(dir)

	// Find an existing editor pane in a session matching the repo
	for _, s := range sessions {
		if !terminal.SessionMatchesRepo(s, dir) {
			continue
		}
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if terminal.IsEditorProcess(p.Command) {
					target := fmt.Sprintf("%s:%d.%d", s.Name, w.Index, p.Index)
					// Reuse: send open command to existing editor
					if err := terminal.SendEditorOpen(a, target, file, line); err != nil {
						return nil, err
					}
					return &terminal.EditorTarget{Session: s.Name, Target: target, Fresh: false}, nil
				}
			}
		}
		// Session exists but no editor — create one
		target, err := a.CreateWindow(s.Name, "editor", dir, terminal.EditorOpenCmd(file, line))
		if err != nil {
			return nil, err
		}
		return &terminal.EditorTarget{Session: s.Name, Target: target, Fresh: true}, nil
	}

	// No matching session — create one, then open editor
	sessName, err := a.CreateSession(dir)
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", dir, err)
	}
	target, err := a.CreateWindow(sessName, "editor", dir, terminal.EditorOpenCmd(file, line))
	if err != nil {
		return nil, fmt.Errorf("creating editor window: %w", err)
	}
	return &terminal.EditorTarget{Session: sessName, Target: target, Fresh: true}, nil
}

// --- Pane output parsing ---

const fieldSep = "\t"
const listFormat = "#{session_name}" + fieldSep +
	"#{session_attached}" + fieldSep +
	"#{window_index}" + fieldSep +
	"#{window_name}" + fieldSep +
	"#{window_active}" + fieldSep +
	"#{pane_index}" + fieldSep +
	"#{pane_pid}" + fieldSep +
	"#{pane_current_path}" + fieldSep +
	"#{pane_current_command}" + fieldSep +
	"#{pane_active}"

func parsePaneOutput(output string) []terminal.Session {
	if output == "" {
		return []terminal.Session{}
	}

	sessionMap := make(map[string]*terminal.Session)
	windowMap := make(map[string]*terminal.Window)
	var sessionOrder []string

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, fieldSep)
		if len(fields) < 10 {
			continue
		}

		sessName := fields[0]
		sessAttached := fields[1] == "1"
		winIdx, _ := strconv.Atoi(fields[2])
		winName := fields[3]
		winActive := fields[4] == "1"
		paneIdx, _ := strconv.Atoi(fields[5])
		panePID, _ := strconv.Atoi(fields[6])
		paneCWD := fields[7]
		paneCmd := fields[8]
		paneActive := fields[9] == "1"

		sess, exists := sessionMap[sessName]
		if !exists {
			sess = &terminal.Session{Name: sessName, Attached: sessAttached}
			sessionMap[sessName] = sess
			sessionOrder = append(sessionOrder, sessName)
		}

		winKey := sessName + ":" + strconv.Itoa(winIdx)
		if _, exists := windowMap[winKey]; !exists {
			win := &terminal.Window{Index: winIdx, Name: winName, Active: winActive}
			windowMap[winKey] = win
			sess.Windows = append(sess.Windows, *win)
		}

		pane := terminal.Pane{
			Index: paneIdx, PID: panePID,
			Active: winActive && paneActive,
			CWD: paneCWD, Command: paneCmd,
		}

		for i := range sess.Windows {
			if sess.Windows[i].Index == winIdx {
				sess.Windows[i].Panes = append(sess.Windows[i].Panes, pane)
				break
			}
		}
	}

	sessions := make([]terminal.Session, 0, len(sessionOrder))
	for _, name := range sessionOrder {
		sessions = append(sessions, *sessionMap[name])
	}
	return sessions
}
