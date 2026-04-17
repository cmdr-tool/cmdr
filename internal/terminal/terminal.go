// Package terminal defines interfaces for pluggable terminal multiplexer
// and emulator backends. Adapters (tmux, cmux, etc.) implement these
// interfaces and register themselves at init time.
package terminal

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Multiplexer abstracts terminal session/window management.
// Implementations live in internal/terminal/adapters/<name>/.
type Multiplexer interface {
	// ListSessions returns all sessions with their windows and panes.
	// Returns an empty slice (not an error) if no sessions exist.
	ListSessions() ([]Session, error)

	// CreateSession creates a new detached session for the given directory.
	// Returns the existing session name if one already exists for that dir.
	CreateSession(dir string) (name string, err error)

	// KillSession destroys a session by name.
	KillSession(name string) error

	// SwitchClient switches the most recently active client to a session.
	SwitchClient(name string) error

	// CreateWindow opens a new window in an existing session with an initial command.
	// Returns a target string identifying the new window (e.g. "session:window").
	CreateWindow(session, windowName, dir, cmd string) (target string, err error)

	// KillWindow destroys a window by target string.
	KillWindow(target string) error

	// SendKeys sends keystrokes to a pane identified by target.
	// If literal is true, keys are sent as typed text with Enter appended.
	SendKeys(target, keys string, literal bool) error

	// CapturePane returns the visible content of a pane (last N lines).
	CapturePane(target string, lines int) (string, error)

	// WindowExists checks whether a window/surface target is still alive.
	WindowExists(target string) bool

	// OpenInEditor opens file:line in the configured editor within a session
	// for dir. Reuses an existing editor pane if possible, otherwise
	// creates a new window. Returns the editor target.
	OpenInEditor(dir, file string, line int) (*EditorTarget, error)
}

// Emulator abstracts bringing a terminal application to the foreground.
type Emulator interface {
	Activate() error
}

// Session represents a terminal session.
type Session struct {
	Name     string   `json:"name"`
	Attached bool     `json:"attached"`
	Windows  []Window `json:"windows"`
}

// Window represents a window containing panes.
type Window struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Panes  []Pane `json:"panes"`
}

// Pane represents a single terminal pane.
type Pane struct {
	Index   int    `json:"index"`
	PID     int    `json:"pid"`
	Active  bool   `json:"active"`
	CWD     string `json:"cwd"`
	Command string `json:"command"`
}

// EditorTarget identifies a pane running an editor.
type EditorTarget struct {
	Session string // e.g. "workers"
	Target  string // e.g. "workers:1.0"
	Fresh   bool   // true if the editor was just launched (file already opened)
}

// --- Adapter registry ---

var (
	mu       sync.RWMutex
	adapters = map[string]func() Multiplexer{}
)

// Register makes a multiplexer adapter available by name.
// Called from adapter init() functions.
func Register(name string, factory func() Multiplexer) {
	mu.Lock()
	defer mu.Unlock()
	adapters[name] = factory
}

// New returns a Multiplexer for the given adapter name.
func New(name string) (Multiplexer, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown terminal adapter: %q", name)
	}
	return factory(), nil
}

// --- Default emulator ---

// MacOSEmulator brings a macOS app to the foreground by name.
type MacOSEmulator struct {
	AppName string // e.g. "Ghostty", "WezTerm", "cmux"
}

func (e *MacOSEmulator) Activate() error {
	return exec.Command("open", "-a", e.AppName).Run()
}

// --- Shared helpers ---

// SessionName computes a consistent session name for a directory.
// Handles git worktrees with a .bare parent ("parent_branch").
// Replaces '.', ' ', '-' with '_'.
func SessionName(dir string) string {
	name := filepath.Base(dir)
	if topLevel, err := gitOutput(dir, "rev-parse", "--show-toplevel"); err == nil {
		parent := filepath.Dir(topLevel)
		bare := filepath.Join(parent, ".bare")
		if isDir(bare) {
			name = filepath.Base(parent) + "_" + filepath.Base(dir)
		}
	}
	r := strings.NewReplacer(".", "_", " ", "_", "-", "_")
	return r.Replace(name)
}

// FindWindowTarget searches sessions for a window matching windowName.
// Checks Window.Name (tmux) and Pane.Command (cmux stores names there).
// Returns the target string and true if found.
func FindWindowTarget(sessions []Session, windowName string) (string, bool) {
	for _, s := range sessions {
		for _, w := range s.Windows {
			if w.Name == windowName {
				return s.Name + ":" + w.Name, true
			}
			for _, p := range w.Panes {
				if p.Command == windowName {
					return s.Name + ":" + windowName, true
				}
			}
		}
	}
	return "", false
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isDir(path string) bool {
	cmd := exec.Command("test", "-d", path)
	return cmd.Run() == nil
}
