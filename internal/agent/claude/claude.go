package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cmdr-tool/cmdr/internal/agent"
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
	args := []string{"-p", cfg.Prompt, "--output-format", "stream-json", "--verbose"}
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cfg.WorkDir

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
