package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/cmdr-tool/cmdr/internal/agent"
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

// DetectInstances finds running pi processes by scanning the process table.
// Pi doesn't write PID-based session files, so we discover instances from `ps`.
func (a *Adapter) DetectInstances() ([]agent.Instance, error) {
	// ps -eo pid,comm — find processes with command "pi"
	out, err := exec.Command("ps", "-eo", "pid,comm").Output()
	if err != nil {
		return []agent.Instance{}, nil
	}

	var instances []agent.Instance
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "pi" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || !isAlive(pid) {
			continue
		}

		cwd := getCWD(pid)
		instances = append(instances, agent.Instance{
			Agent:   "pi",
			PID:     pid,
			CWD:     cwd,
			Project: filepath.Base(cwd),
			Status:  "unknown",
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
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}

	for _, line := range tail {
		if strings.Contains(line, "escape interrupt") {
			return "working"
		}
	}

	// Pi's idle prompt shows the path and model info at the bottom
	for _, line := range tail {
		if strings.Contains(line, "gpt-") || strings.Contains(line, "claude-") || strings.Contains(line, "gemini") {
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

// getCWD resolves the current working directory for a process via lsof.
func getCWD(pid int) string {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn", "-d", "cwd").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return line[1:]
		}
	}
	return ""
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
