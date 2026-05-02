// Package ollama wraps the existing Ollama client as a summarizer adapter.
package ollama

import (
	"context"

	"github.com/cmdr-tool/cmdr/internal/ollama"
	"github.com/cmdr-tool/cmdr/internal/summarizer"
)

func init() {
	summarizer.Register("ollama", func() summarizer.Summarizer {
		return &Adapter{}
	})
}

// Adapter delegates to the existing internal/ollama package.
type Adapter struct{}

func (a *Adapter) Summarize(ctx context.Context, content, hint string) (string, error) {
	return ollama.Summarize(ctx, content, hint)
}
