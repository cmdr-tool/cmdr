// Package apple implements the summarizer.Summarizer interface using
// Apple Intelligence via the cmdr-summarize Swift binary.
package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cmdr-tool/cmdr/internal/summarizer"
)

func init() {
	summarizer.Register("apple", func() summarizer.Summarizer {
		return New()
	})
}

// Adapter spawns cmdr-summarize to generate titles on-device.
type Adapter struct {
	binPath string
}

// New returns an Apple Intelligence adapter.
// Binary discovery: same directory as cmdr binary → PATH.
func New() *Adapter {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "cmdr-summarize")
		if _, err := os.Stat(candidate); err == nil {
			return &Adapter{binPath: candidate}
		}
	}
	return &Adapter{binPath: "cmdr-summarize"}
}

func (a *Adapter) Summarize(ctx context.Context, content string) (string, error) {
	cmd := exec.CommandContext(ctx, a.binPath)
	cmd.Stdin = bytes.NewBufferString(content)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cmdr-summarize: %w", err)
	}

	var result struct {
		Title string `json:"title"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return "", fmt.Errorf("cmdr-summarize: parse: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("cmdr-summarize: %s", result.Error)
	}
	return result.Title, nil
}
