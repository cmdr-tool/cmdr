package agent

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
)

// Agent abstracts an AI coding agent (Claude, Codex, pi.dev, etc.).
// All methods except those gated by Capabilities are mandatory.
type Agent interface {
	// Name returns the adapter's registered name (e.g. "claude", "codex").
	Name() string

	// Capabilities describes optional features this agent supports.
	Capabilities() Capabilities

	// RunSimple executes a one-shot headless prompt and returns the full output.
	RunSimple(ctx context.Context, cfg SimpleConfig) (string, error)

	// RunStreaming executes a headless prompt with streaming output.
	// Calls onEvent for each parsed event. Returns the final result.
	RunStreaming(ctx context.Context, cfg StreamingConfig, onEvent func(StreamEvent)) (*StreamResult, error)

	// InteractiveCommand returns the shell command string to launch an
	// interactive session in a terminal window.
	InteractiveCommand(cfg InteractiveConfig) (string, error)

	// ResumeCommand returns the shell command string to resume a prior session.
	ResumeCommand(sessionID string) (string, error)

	// ProcessName returns the binary name to match in terminal pane commands.
	// Used by the poller to identify which panes are running this agent.
	ProcessName() string

	// DetectInstances returns currently running instances of this agent.
	// Used by the poller for process-to-pane matching and the "unmatched instances" UI.
	DetectInstances() ([]Instance, error)

	// PaneStatus determines the agent's status from captured terminal pane output.
	// Returns "working", "waiting", "idle", or "unknown".
	PaneStatus(lines []string) string
}

// Capabilities describes optional features an agent supports.
// Interactive, Resume, and Headless are mandatory for all agents.
type Capabilities struct {
	Streaming bool `json:"streaming"` // incremental output events vs just final result
	Worktrees bool `json:"worktrees"` // can isolate work in git worktrees
}

// SimpleConfig configures a one-shot headless execution.
type SimpleConfig struct {
	Prompt       string
	WorkDir      string
	AllowedTools []string
}

// StreamingConfig configures a streaming headless execution.
type StreamingConfig struct {
	Prompt       string
	WorkDir      string
	SystemPrompt string
}

// StreamEvent is a normalized event emitted during streaming.
// Each agent adapter maps its wire format into these.
type StreamEvent struct {
	Type   string // "text", "tool", "error"
	Text   string // for Type=="text"
	Tool   string // for Type=="tool": tool name
	Detail string // for Type=="tool": human-readable detail
}

// StreamResult is the final output from a streaming run.
type StreamResult struct {
	Output    string
	SessionID string    // agent-specific session ID for resume (empty if not supported)
	Cmd       *exec.Cmd // for cancellation tracking
}

// Instance represents a running agent process detected by the adapter.
type Instance struct {
	Agent     string `json:"agent"`               // adapter name
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId,omitempty"`
	CWD       string `json:"cwd"`
	Project   string `json:"project"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
	Status    string `json:"status"`              // "working", "waiting", "idle", "unknown"
	TmuxTarget string `json:"tmuxTarget,omitempty"`
}

// InteractiveConfig configures an interactive terminal session launch.
type InteractiveConfig struct {
	WorktreeName string // empty = no worktree
	TaskName     string // e.g. "cmdr-task-42"
	SystemPrompt string
	PromptFile   string // path to file piped via stdin
}

// --- Adapter registry ---

var (
	mu       sync.RWMutex
	adapters = map[string]func() Agent{}
)

// Register makes an agent adapter available by name.
// Called from adapter init() functions.
func Register(name string, factory func() Agent) {
	mu.Lock()
	defer mu.Unlock()
	adapters[name] = factory
}

// New returns an Agent for the given adapter name.
func New(name string) (Agent, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent: %q (available: %v)", name, available())
	}
	return factory(), nil
}

func available() []string {
	names := make([]string, 0, len(adapters))
	for k := range adapters {
		names = append(names, k)
	}
	return names
}

// All returns an instance of every registered adapter.
func All() []Agent {
	mu.RLock()
	defer mu.RUnlock()
	agents := make([]Agent, 0, len(adapters))
	for _, factory := range adapters {
		agents = append(agents, factory())
	}
	return agents
}
