package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/proc"
)

func init() {
	agent.Register("claude", func() agent.Agent {
		return &Adapter{}
	})
}

// Adapter implements agent.Agent for the Claude Code CLI.
type Adapter struct{}

func (a *Adapter) Name() string { return "claude" }

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		Streaming: true,
		Worktrees: true,
	}
}

// RunSimple executes claude -p and returns the full output.
func (a *Adapter) RunSimple(ctx context.Context, cfg agent.SimpleConfig) (string, error) {
	args := []string{"-p", cfg.Prompt}
	for _, tool := range cfg.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("claude: %w\n%s", err, out)
	}
	return string(out), nil
}

// RunStreaming executes claude -p with --output-format stream-json,
// parsing events and calling onEvent for each text/tool block.
func (a *Adapter) RunStreaming(ctx context.Context, cfg agent.StreamingConfig, onEvent func(agent.StreamEvent)) (*agent.StreamResult, error) {
	var args []string
	if cfg.PromptFile != "" {
		args = []string{"-p", "-", "--output-format", "stream-json", "--verbose"}
	} else {
		args = []string{"-p", cfg.Prompt, "--output-format", "stream-json", "--verbose"}
	}
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cfg.WorkDir

	if cfg.PromptFile != "" {
		f, err := os.Open(cfg.PromptFile)
		if err != nil {
			return nil, fmt.Errorf("claude: open prompt file: %w", err)
		}
		defer f.Close()
		cmd.Stdin = f
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude start: %w", err)
	}

	var finalResult, sessionID string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}

		evtType, _ := evt["type"].(string)

		switch evtType {
		case "assistant":
			msg, _ := evt["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch b["type"] {
				case "text":
					if text, ok := b["text"].(string); ok && text != "" {
						onEvent(agent.StreamEvent{Type: "text", Text: text})
					}
				case "tool_use":
					name, _ := b["name"].(string)
					if name != "" {
						onEvent(agent.StreamEvent{
							Type:   "tool",
							Tool:   name,
							Detail: toolDetail(name, b["input"]),
						})
					}
				}
			}

		case "result":
			if r, ok := evt["result"].(string); ok {
				finalResult = r
			}
			if sid, ok := evt["session_id"].(string); ok {
				sessionID = sid
			}
		}
	}

	if err := cmd.Wait(); err != nil && finalResult == "" {
		return nil, fmt.Errorf("claude exited: %w", err)
	}

	if finalResult == "" {
		return nil, fmt.Errorf("no result from claude")
	}

	return &agent.StreamResult{
		Output:    finalResult,
		SessionID: sessionID,
		Cmd:       cmd,
	}, nil
}

// InteractiveCommand returns the shell command to launch an interactive
// Claude session in a terminal window.
func (a *Adapter) InteractiveCommand(cfg agent.InteractiveConfig) (string, error) {
	var baseCmd string
	if cfg.WorktreeName != "" {
		baseCmd = fmt.Sprintf("claude -w %s --name '%s'", cfg.WorktreeName, cfg.TaskName)
	} else {
		baseCmd = fmt.Sprintf("claude --name '%s'", cfg.TaskName)
	}

	if cfg.SystemPrompt != "" {
		escaped := strings.ReplaceAll(cfg.SystemPrompt, "'", "'\\''")
		return fmt.Sprintf("exec %s --append-system-prompt '%s' < '%s'", baseCmd, escaped, cfg.PromptFile), nil
	}
	return fmt.Sprintf("exec %s < '%s'", baseCmd, cfg.PromptFile), nil
}

// ResumeCommand returns the shell command to resume a prior Claude session.
func (a *Adapter) ResumeCommand(sessionID string) (string, error) {
	return fmt.Sprintf("exec claude --resume '%s'", sessionID), nil
}

// --- Detection ---

func (a *Adapter) ProcessName() string { return "claude" }

// DetectInstances reads ~/.claude/sessions/*.json for active Claude sessions.
func (a *Adapter) DetectInstances(_ *proc.Snapshot) ([]agent.Instance, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(home, ".claude", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []agent.Instance{}, nil
		}
		return nil, err
	}

	var instances []agent.Instance
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		var raw struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			StartedAt int64  `json:"startedAt"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}

		if !isAlive(raw.PID) {
			continue
		}

		var uptime string
		if raw.StartedAt > 0 {
			uptime = formatUptime(time.Since(time.UnixMilli(raw.StartedAt)))
		}

		instances = append(instances, agent.Instance{
			Agent:     "claude",
			PID:       raw.PID,
			SessionID: raw.SessionID,
			CWD:       raw.CWD,
			Project:   filepath.Base(raw.CWD),
			StartedAt: raw.StartedAt,
			Uptime:    uptime,
			Status:    "unknown",
		})
	}

	if instances == nil {
		instances = []agent.Instance{}
	}
	return instances, nil
}

// PaneStatus determines Claude's status from captured terminal pane lines.
func (a *Adapter) PaneStatus(lines []string) string {
	// Scan the last few lines for hint text signals
	tail := lines
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}

	statusTracker.mu.Lock()
	defer statusTracker.mu.Unlock()

	// Build a key from the lines for tracker (use first line as proxy)
	key := ""
	if len(lines) > 0 {
		key = lines[0]
	}

	for _, line := range tail {
		if strings.Contains(line, workingSignal) {
			statusTracker.lastWorking[key] = time.Now()
			return "working"
		}
	}

	for _, line := range tail {
		if strings.Contains(line, idleSignal) {
			lastWork, exists := statusTracker.lastWorking[key]
			if exists && time.Since(lastWork) < idleThreshold {
				return "waiting"
			}
			return "idle"
		}
	}

	for _, line := range tail {
		for _, sig := range waitingSignals {
			if strings.Contains(line, sig) {
				return "waiting"
			}
		}
	}

	return "unknown"
}

// Claude pane hint text signals
const (
	workingSignal = "esc to interrupt"
	idleSignal    = "hold Space to speak"
	idleThreshold = 5 * time.Minute
)

var waitingSignals = []string{
	"accept edits",
	"to accept",
	"to reject",
	"shift+tab to cycle",
}

var statusTracker = struct {
	mu          sync.Mutex
	lastWorking map[string]time.Time
}{
	lastWorking: make(map[string]time.Time),
}

func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func formatUptime(d time.Duration) string {
	return proc.FormatUptime(d)
}

// toolDetail extracts a human-readable detail string from a tool_use input.
func toolDetail(name string, input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	switch name {
	case "Read":
		if p, ok := m["file_path"].(string); ok {
			if i := strings.Index(p, "ThoughtQuarry/"); i >= 0 {
				return p[i+len("ThoughtQuarry/"):]
			}
			return p
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	}
	return ""
}
