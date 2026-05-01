// Package agentoverride loads and resolves user-configured agent overrides
// for specific headless task types. Override files live at
// ~/.cmdr/agents/<task-type>.md and use frontmatter to declare the agent
// adapter and output format; the body becomes the system prompt.
//
// Example ~/.cmdr/agents/trace.md:
//
//	---
//	agent: pi
//	output: markdown
//	---
//
//	You are an architect analyzing this codebase. Trace data flows...
//
// Loaded once via Load() (typically at daemon startup). Resolve() returns
// the override-or-default agent, system prompt, and output format for a
// given task type.
package agentoverride

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

// registry holds the loaded overrides keyed by task type. Populated by
// Load() and read by Resolve()/Lookup().
var registry map[string]Override

// Load reads all .md files from ~/.cmdr/agents/ and parses frontmatter.
// Idempotent — calling multiple times overwrites the registry. Adapters
// must already be registered (via blank imports) before this is called,
// since validation calls agent.New().
func Load() {
	registry = make(map[string]Override)

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

		if _, err := agent.New(ov.Agent); err != nil {
			log.Printf("cmdr: override %q: unknown agent %q, ignoring", taskType, ov.Agent)
			continue
		}

		registry[taskType] = ov
		log.Printf("cmdr: loaded override %q (agent=%s, output=%s)", taskType, ov.Agent, ov.OutputFormat)
	}
}

// Resolve returns the agent, system prompt, and output format for a task type.
// If an override exists, returns the override's agent + prompt + format.
// Otherwise returns an instance of defaultAgentName with empty prompt and
// "markdown" format. Returns nil agent only if defaultAgentName is also unknown.
func Resolve(taskType, defaultAgentName string) (agent.Agent, string, string) {
	if ov, ok := registry[taskType]; ok {
		a, err := agent.New(ov.Agent)
		if err == nil {
			return a, ov.Prompt, ov.OutputFormat
		}
		log.Printf("cmdr: override %q: agent %q unavailable, using default", taskType, ov.Agent)
	}
	a, err := agent.New(defaultAgentName)
	if err != nil {
		return nil, "", "markdown"
	}
	return a, "", "markdown"
}

// Lookup returns the raw override for a task type if one exists. Useful
// when callers need to inspect override metadata without resolving the agent.
func Lookup(taskType string) (Override, bool) {
	ov, ok := registry[taskType]
	return ov, ok
}

// parseOverride parses a frontmatter + body markdown file into an Override.
func parseOverride(content string) (Override, error) {
	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		return Override{}, fmt.Errorf("missing frontmatter (must start with ---)")
	}

	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return Override{}, fmt.Errorf("missing closing --- in frontmatter")
	}

	header := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])

	var ov Override
	ov.OutputFormat = "markdown"

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
