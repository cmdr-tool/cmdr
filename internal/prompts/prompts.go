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

// Intent represents a predefined directive intent.
type Intent struct {
	ID         string `json:"id"`         // e.g. "bug-fix"
	Name       string `json:"name"`       // e.g. "Bug Fix"
	ProducesPR bool   `json:"producesPR"` // true if this intent is expected to create a PR
}

// Intents that are expected to produce a PR as their artifact.
var prIntents = map[string]bool{
	"refactor":    true,
	"bug-fix":     true,
	"new-feature": true,
}

// ListIntents returns all available intent presets.
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
		name := strings.ReplaceAll(id, "-", " ")
		// Title case
		words := strings.Fields(name)
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		intents = append(intents, Intent{ID: id, Name: strings.Join(words, " "), ProducesPR: prIntents[id]})
	}
	return intents
}

// IntentProducesPR returns whether the given intent is expected to produce a PR.
func IntentProducesPR(id string) bool {
	return prIntents[id]
}

// GetIntentPrompt returns the system prompt for a given intent ID.
func GetIntentPrompt(id string) (string, error) {
	data, err := intentFS.ReadFile("intents/" + id + ".md")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
