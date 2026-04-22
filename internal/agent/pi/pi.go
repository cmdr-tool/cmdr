package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/proc"
)

func init() {
	agent.Register("pi", func() agent.Agent {
		return &Adapter{}
	})
}

// Adapter implements agent.Agent for the pi.dev CLI.
type Adapter struct{}

func (a *Adapter) Name() string { return "pi" }

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		Streaming: true,
		Worktrees: false,
	}
}

// RunSimple executes pi -p and returns the full output.
func (a *Adapter) RunSimple(ctx context.Context, cfg agent.SimpleConfig) (string, error) {
	args := []string{"-p", cfg.Prompt}
	if len(cfg.AllowedTools) > 0 {
		args = append(args, "--tools", strings.Join(cfg.AllowedTools, ","))
	}

	cmd := exec.CommandContext(ctx, "pi", args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pi: %w\n%s", err, out)
	}
	return string(out), nil
}

// RunStreaming executes pi -p --mode json, parsing events and calling
// onEvent for each text/tool block.
func (a *Adapter) RunStreaming(ctx context.Context, cfg agent.StreamingConfig, onEvent func(agent.StreamEvent)) (*agent.StreamResult, error) {
	args := []string{"-p", cfg.Prompt, "--mode", "json"}
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}

	cmd := exec.CommandContext(ctx, "pi", args...)
	cmd.Dir = cfg.WorkDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pi start: %w", err)
	}

	var sessionID, finalText string
	var currentToolName string
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
		case "session":
			if id, ok := evt["id"].(string); ok {
				sessionID = id
			}

		case "message_update":
			aEvt, _ := evt["assistantMessageEvent"].(map[string]any)
			if aEvt == nil {
				continue
			}
			aType, _ := aEvt["type"].(string)

			switch aType {
			case "text_delta":
				if delta, ok := aEvt["delta"].(string); ok && delta != "" {
					// Accumulate full text for final result
					finalText += delta
					// Emit the full accumulated text (matching Claude adapter behavior)
					onEvent(agent.StreamEvent{Type: "text", Text: finalText})
				}

			case "toolcall_start":
				// Extract tool name from the message content
				if msg, ok := aEvt["message"].(map[string]any); ok {
					if content, ok := msg["content"].([]any); ok && len(content) > 0 {
						if block, ok := content[0].(map[string]any); ok {
							currentToolName, _ = block["name"].(string)
						}
					}
				}

			case "toolcall_end":
				if tc, ok := aEvt["toolCall"].(map[string]any); ok {
					name, _ := tc["name"].(string)
					if name == "" {
						name = currentToolName
					}
					detail := toolDetail(name, tc["arguments"])
					onEvent(agent.StreamEvent{
						Type:   "tool",
						Tool:   name,
						Detail: detail,
					})
				}
				currentToolName = ""

			case "text_start":
				// Reset accumulated text for new text block
				finalText = ""
			}

		case "agent_end":
			// Extract final text from the last assistant message
			if msgs, ok := evt["messages"].([]any); ok {
				for i := len(msgs) - 1; i >= 0; i-- {
					msg, ok := msgs[i].(map[string]any)
					if !ok {
						continue
					}
					role, _ := msg["role"].(string)
					if role != "assistant" {
						continue
					}
					content, _ := msg["content"].([]any)
					for _, block := range content {
						b, ok := block.(map[string]any)
						if !ok {
							continue
						}
						if b["type"] == "text" {
							if text, ok := b["text"].(string); ok {
								finalText = text
							}
						}
					}
					break
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil && finalText == "" {
		return nil, fmt.Errorf("pi exited: %w", err)
	}

	if finalText == "" {
		return nil, fmt.Errorf("no result from pi")
	}

	return &agent.StreamResult{
		Output:    finalText,
		SessionID: sessionID,
		Cmd:       cmd,
	}, nil
}

// InteractiveCommand returns the shell command to launch an interactive
// pi session in a terminal window.
func (a *Adapter) InteractiveCommand(cfg agent.InteractiveConfig) (string, error) {
	// pi doesn't support worktrees or --name, so we just set up the basic command
	baseCmd := "pi"

	if cfg.SystemPrompt != "" {
		escaped := strings.ReplaceAll(cfg.SystemPrompt, "'", "'\\''")
		return fmt.Sprintf("exec %s --append-system-prompt '%s' < '%s'", baseCmd, escaped, cfg.PromptFile), nil
	}
	return fmt.Sprintf("exec %s < '%s'", baseCmd, cfg.PromptFile), nil
}

// ResumeCommand returns the shell command to resume a prior pi session.
func (a *Adapter) ResumeCommand(sessionID string) (string, error) {
	return fmt.Sprintf("exec pi --session '%s'", sessionID), nil
}

// --- Detection ---

func (a *Adapter) ProcessName() string { return "pi" }

// DetectInstances finds interactive pi processes from a shared process snapshot.
// Pi often forks a child pi process, so we keep only root pi processes with a TTY.
func (a *Adapter) DetectInstances(snapshot *proc.Snapshot) ([]agent.Instance, error) {
	if snapshot == nil {
		var err error
		snapshot, err = proc.List()
		if err != nil {
			return nil, err
		}
	}

	var instances []agent.Instance
	for _, p := range snapshot.Processes() {
		if !isPiProcess(p) || !isInteractiveTTY(p.TTY) || !isAlive(p.PID) {
			continue
		}
		if parent, ok := snapshot.Process(p.PPID); ok && isPiProcess(parent) {
			continue // nested pi child of the same interactive run
		}

		cwd := proc.Cwd(p.PID)
		startedAt := time.Now().Add(-p.Elapsed).UnixMilli()
		instances = append(instances, agent.Instance{
			Agent:     "pi",
			PID:       p.PID,
			CWD:       cwd,
			Project:   filepath.Base(cwd),
			StartedAt: startedAt,
			Uptime:    proc.FormatUptime(p.Elapsed),
			Status:    "unknown",
		})
	}

	if instances == nil {
		instances = []agent.Instance{}
	}
	return instances, nil
}

// PaneStatus determines pi's status from captured terminal pane lines.
func (a *Adapter) PaneStatus(lines []string) string {
	tail := lines
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}

	for _, line := range tail {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "working...") || strings.Contains(lower, "working…") || strings.Contains(lower, "running...") || strings.Contains(lower, "running…") || strings.Contains(lower, "escape interrupt") {
			return "working"
		}
	}

	if hasPiFooter(tail) {
		return "idle"
	}

	for _, line := range tail {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "ctrl+o to expand") || strings.Contains(lower, "ctrl+o") {
			return "idle"
		}
	}

	return "unknown"
}

func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func isPiProcess(p proc.Process) bool {
	if p.Comm == "pi" {
		return true
	}
	return proc.BaseCommand(p.Args) == "pi"
}

func isInteractiveTTY(tty string) bool {
	tty = strings.TrimSpace(tty)
	return tty != "" && tty != "??"
}

func hasPiFooter(lines []string) bool {
	for i := 0; i < len(lines)-1; i++ {
		pwdLine := strings.TrimSpace(lines[i])
		statsLine := strings.TrimSpace(lines[i+1])
		if pwdLine == "" || statsLine == "" {
			continue
		}
		if looksLikePiPwdLine(pwdLine) && looksLikePiStatsLine(statsLine) {
			return true
		}
	}
	return false
}

func looksLikePiPwdLine(line string) bool {
	if strings.HasPrefix(line, "/") || strings.HasPrefix(line, "~") {
		return true
	}
	return false
}

func looksLikePiStatsLine(line string) bool {
	lower := strings.ToLower(line)
	if !strings.Contains(line, "/") {
		return false
	}
	if !(strings.Contains(lower, "gpt") || strings.Contains(lower, "claude") || strings.Contains(lower, "gemini") || strings.Contains(lower, "codex") || strings.Contains(lower, "no-model")) {
		return false
	}
	if !strings.Contains(line, "•") && !strings.Contains(lower, "thinking off") {
		return false
	}
	return strings.Contains(line, "↑") || strings.Contains(line, "↓") || strings.Contains(line, "$") || strings.Contains(lower, "(sub)")
}

// toolDetail extracts a human-readable detail string from tool arguments.
func toolDetail(name string, args any) string {
	m, ok := args.(map[string]any)
	if !ok {
		return ""
	}
	switch name {
	case "bash":
		if cmd, ok := m["command"].(string); ok {
			if len(cmd) > 60 {
				return cmd[:57] + "..."
			}
			return cmd
		}
	case "read":
		if p, ok := m["file_path"].(string); ok {
			return p
		}
	case "edit":
		if p, ok := m["file_path"].(string); ok {
			return p
		}
	case "write":
		if p, ok := m["file_path"].(string); ok {
			return p
		}
	case "grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "find":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	}
	return ""
}
