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

	// CandidatePanes returns all panes that could potentially host agent
	// processes. Adapters populate required fields from their data model
	// and can stash adapter-specific metadata in Meta for use during
	// MatchInstances.
	CandidatePanes(sessions []Session) []CandidatePane

	// MatchInstances resolves which agent processes are running in which
	// panes. The adapter owns the matching loop and uses whatever strategy
	// fits its data model (PID ancestry for tmux, CWD/session-name for
	// cmux). ppidMap provides process parent-child relationships.
	MatchInstances(procs []AgentProcess, panes []CandidatePane, ppidMap map[int]int) []InstanceMatch
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

// CandidatePane represents a terminal pane that could be running an agent.
// Required fields are populated by every adapter; Meta carries adapter-specific
// data that gets passed back during MatchInstances.
type CandidatePane struct {
	Target  string // e.g. "cmdr:1.3" — identifier for CapturePane/SendKeys
	Session string // session name for grouping
	PID     int    // shell PID (0 if unavailable)
	CWD     string // working directory (empty if unavailable)
	Command string // current command (empty if unavailable)
	Meta    any    // adapter-specific data, opaque to the poller
}

// AgentProcess describes a detected agent instance for matching purposes.
type AgentProcess struct {
	Index       int    // index into the original instances slice
	PID         int    // agent process PID
	CWD         string // working directory
	Project     string // project name (basename of CWD)
	ProcessName string // binary name ("claude", "pi")
}

// InstanceMatch is returned by MatchInstances when an agent process
// is successfully matched to a terminal pane.
type InstanceMatch struct {
	ProcessIndex int    // index back into the AgentProcess/instances slice
	Target       string // matched pane target
	CWD          string // resolved CWD (from pane if agent didn't have one)
}

// ForEachPane iterates all panes across sessions, calling fn with the
// constructed target string and a pointer to the pane.
func ForEachPane(sessions []Session, fn func(target string, p *Pane)) {
	for si := range sessions {
		for wi := range sessions[si].Windows {
			for pi := range sessions[si].Windows[wi].Panes {
				pane := &sessions[si].Windows[wi].Panes[pi]
				target := fmt.Sprintf("%s:%d.%d", sessions[si].Name, sessions[si].Windows[wi].Index, pane.Index)
				fn(target, pane)
			}
		}
	}
}

// MatchByPIDAncestry is a shared matching strategy for adapters with PID data.
// Builds a shellPID lookup from candidate panes, then walks the PPID tree from
// each agent process upward to find ancestor shell PIDs.
func MatchByPIDAncestry(procs []AgentProcess, panes []CandidatePane, ppidMap map[int]int) []InstanceMatch {
	// Build shell PID → pane index map from candidates that matched by
	// command name or had a known agent PID as descendant.
	type paneInfo struct {
		target string
		cwd    string
	}

	// First pass: collect panes whose command matches an agent process name
	// or whose PID is an ancestor of an agent PID.
	agentPIDs := make(map[int]bool)
	processNames := make(map[string]bool)
	for _, p := range procs {
		agentPIDs[p.PID] = true
		processNames[p.ProcessName] = true
	}

	paneAncestor := func(panePID int) bool {
		for aPID := range agentPIDs {
			visited := make(map[int]bool)
			for cur := aPID; cur > 1 && !visited[cur]; cur = ppidMap[cur] {
				visited[cur] = true
				if cur == panePID {
					return true
				}
			}
		}
		return false
	}

	shellPIDs := make(map[int]*paneInfo)
	for i := range panes {
		if panes[i].PID == 0 {
			continue
		}
		if processNames[panes[i].Command] || paneAncestor(panes[i].PID) {
			shellPIDs[panes[i].PID] = &paneInfo{target: panes[i].Target, cwd: panes[i].CWD}
		}
	}

	// Second pass: for each agent process, walk PPID chain to find a shell
	var matches []InstanceMatch
	for _, proc := range procs {
		visited := make(map[int]bool)
		for cur := proc.PID; cur > 1 && !visited[cur]; cur = ppidMap[cur] {
			visited[cur] = true
			if pi, ok := shellPIDs[cur]; ok {
				matches = append(matches, InstanceMatch{
					ProcessIndex: proc.Index,
					Target:       pi.target,
					CWD:          pi.cwd,
				})
				break
			}
		}
	}
	return matches
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
