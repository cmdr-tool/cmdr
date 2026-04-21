package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmdr-tool/cmdr/internal/agent"
)

// Override represents a user-configured agent override for a task type.
type Override struct {
	Agent        string // registered adapter name (e.g. "pi", "codex")
	OutputFormat string // normalized: "markdown", "html", or "text"
	Prompt       string // custom system prompt (body of the .md file)
}

// overrides maps task type (e.g. "review", "analysis") to override config.
var overrides map[string]Override

// loadOverrides reads all .md files from ~/.cmdr/agents/ and parses frontmatter.
func loadOverrides() {
	overrides = make(map[string]Override)

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	dir := filepath.Join(home, ".cmdr", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // directory doesn't exist — no overrides
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		taskType := strings.TrimSuffix(e.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("cmdr: override %q: read error: %v", taskType, err)
			continue
		}

		ov, err := parseOverride(string(data))
		if err != nil {
			log.Printf("cmdr: override %q: %v", taskType, err)
			continue
		}

		// Validate the agent exists
		if _, err := agent.New(ov.Agent); err != nil {
			log.Printf("cmdr: override %q: unknown agent %q, ignoring", taskType, ov.Agent)
			continue
		}

		overrides[taskType] = ov
		log.Printf("cmdr: loaded override %q (agent=%s, output=%s)", taskType, ov.Agent, ov.OutputFormat)
	}
}

// resolveAgent returns the agent, system prompt, and output format for a task type.
// If an override exists, returns the override's agent + prompt.
// Otherwise returns the default agent with empty prompt and "markdown" format.
func resolveAgent(taskType string) (agent.Agent, string, string) {
	ov, ok := overrides[taskType]
	if !ok {
		return agt, "", "markdown"
	}

	a, err := agent.New(ov.Agent)
	if err != nil {
		// Should not happen (validated at load), but fall back
		log.Printf("cmdr: override %q: agent %q unavailable, using default", taskType, ov.Agent)
		return agt, "", "markdown"
	}

	return a, ov.Prompt, ov.OutputFormat
}

// parseOverride parses a frontmatter + body markdown file into an Override.
func parseOverride(content string) (Override, error) {
	content = strings.TrimSpace(content)

	// Must start with ---
	if !strings.HasPrefix(content, "---") {
		return Override{}, fmt.Errorf("missing frontmatter (must start with ---)")
	}

	// Find closing ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return Override{}, fmt.Errorf("missing closing --- in frontmatter")
	}

	header := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])

	var ov Override
	ov.OutputFormat = "markdown" // default

	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "agent":
			ov.Agent = val
		case "output":
			ov.OutputFormat = normalizeOutputFormat(val)
		}
	}

	if ov.Agent == "" {
		return Override{}, fmt.Errorf("missing required 'agent' field in frontmatter")
	}

	ov.Prompt = body
	return ov, nil
}

// normalizeOutputFormat maps aliases to canonical format names.
func normalizeOutputFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "md", "markdown":
		return "markdown"
	case "html":
		return "html"
	case "text", "plain":
		return "text"
	default:
		return "markdown"
	}
}
