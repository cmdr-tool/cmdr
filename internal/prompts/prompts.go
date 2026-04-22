package prompts

import (
	"bytes"
	"embed"
	"io/fs"
	"strings"
	"text/template"
)

//go:embed *.md
var promptFS embed.FS

//go:embed intents/*.md
var intentFS embed.FS

var templates *template.Template

func init() {
	templates = template.Must(template.ParseFS(promptFS, "*.md"))
}

// ReviewAnnotation represents a reviewer's comment on specific diff lines.
type ReviewAnnotation struct {
	LineStart int
	LineEnd   int
	Context   string // the actual diff lines in the range
	Comment   string
}

// ReviewData is the template data for review.md.
type ReviewData struct {
	RepoName    string
	SHA         string
	Author      string
	Date        string
	Message     string
	Diff        string
	Annotations []ReviewAnnotation
	CommitNote  string // general reviewer note (not tied to specific lines)
}

// Review renders the review prompt template.
func Review(data ReviewData) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "review.md", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// --- Intent metadata registry ---

// IntentMeta describes the execution characteristics of a directive intent.
type IntentMeta struct {
	Mode     string // "interactive" (tmux window) or "headless" (claude -p)
	Artifact string // expected output: "pr", "adr", "report", or "" (none)
	Worktree bool   // true = isolate in a git worktree
	Hidden   bool   // true = not shown in directive intent picker UI
}

// intentRegistry is the single source of truth for intent behavior.
var intentRegistry = map[string]IntentMeta{
	"bug-fix":        {Mode: "interactive", Artifact: "pr", Worktree: true},
	"refactor":       {Mode: "interactive", Artifact: "pr", Worktree: true},
	"new-feature":    {Mode: "interactive", Artifact: "adr", Worktree: true},
	"analysis":       {Mode: "headless", Artifact: "report"},
	"implementation": {Mode: "interactive", Artifact: "pr", Worktree: true, Hidden: true},
	"delegation":     {Mode: "interactive", Worktree: true, Hidden: true},
	"generic":        {Mode: "interactive", Hidden: true},
}

// GetIntentMeta returns the metadata for an intent, or a zero-value IntentMeta
// for unknown intents (which behave as interactive with no expected artifact).
func GetIntentMeta(id string) IntentMeta {
	return intentRegistry[id]
}

// Intent represents a user-facing directive intent for the UI.
type Intent struct {
	ID         string `json:"id"`         // e.g. "bug-fix"
	Name       string `json:"name"`       // e.g. "Bug Fix"
	ProducesPR bool   `json:"producesPR"` // true if artifact is "pr"
}

// ListIntents returns all user-facing intent presets.
func ListIntents() []Intent {
	var intents []Intent
	entries, err := fs.ReadDir(intentFS, "intents")
	if err != nil {
		return intents
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		meta := GetIntentMeta(id)
		if meta.Hidden {
			continue
		}
		name := strings.ReplaceAll(id, "-", " ")
		words := strings.Fields(name)
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		intents = append(intents, Intent{
			ID:         id,
			Name:       strings.Join(words, " "),
			ProducesPR: meta.Artifact == "pr",
		})
	}
	return intents
}

// --- Convenience wrappers (thin delegates to IntentMeta) ---

// IntentProducesPR returns whether the intent is expected to produce a PR.
func IntentProducesPR(id string) bool {
	return GetIntentMeta(id).Artifact == "pr"
}

// IntentIsHeadless returns whether the intent runs via claude -p (no tmux window).
func IntentIsHeadless(id string) bool {
	return GetIntentMeta(id).Mode == "headless"
}

// TaskIsHeadless returns whether a task runs headlessly based on its type and intent.
// Some task types (ask, review, revision) are always headless regardless of intent.
func TaskIsHeadless(taskType, intent string) bool {
	switch taskType {
	case "ask", "review", "revision":
		return true
	default:
		return IntentIsHeadless(intent)
	}
}

// IntentHasDesignPhase returns whether an intent produces an ADR before implementation.
func IntentHasDesignPhase(id string) bool {
	return GetIntentMeta(id).Artifact == "adr"
}

// --- Prompt loading ---

// GetIntentPrompt returns the system prompt for a given intent ID.
func GetIntentPrompt(id string) (string, error) {
	data, err := intentFS.ReadFile("intents/" + id + ".md")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetDesignPrompt returns the design-phase system prompt for an intent.
// Returns empty string if the intent has no design phase.
// TODO: Remove once new-feature.md IS the design prompt (Phase 5 cleanup).
func GetDesignPrompt(id string) (string, error) {
	if GetIntentMeta(id).Artifact != "adr" {
		return "", nil
	}
	// For now, still load design.md for ADR-producing intents
	data, err := promptFS.ReadFile("design.md")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
