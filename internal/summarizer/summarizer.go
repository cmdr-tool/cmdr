// Package summarizer defines a pluggable interface for generating concise
// titles from task content. Adapters (Apple Intelligence, Ollama, etc.)
// register themselves at init time. Selected via CMDR_SUMMARIZER env var.
package summarizer

import (
	"context"
	"fmt"
	"sync"
)

// Summarizer generates a short title from content. The optional hint is
// extra guidance layered onto the adapter's built-in instruction — useful
// for nudging the model away from default phrasings (e.g. preventing trace
// titles from all starting with "Trace ..."). Pass "" when no guidance
// is needed.
type Summarizer interface {
	Summarize(ctx context.Context, content, hint string) (string, error)
}

// --- Adapter registry ---

var (
	mu       sync.RWMutex
	adapters = map[string]func() Summarizer{}
)

// Register makes a summarizer adapter available by name.
func Register(name string, factory func() Summarizer) {
	mu.Lock()
	defer mu.Unlock()
	adapters[name] = factory
}

// New returns a Summarizer for the given adapter name.
func New(name string) (Summarizer, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown summarizer: %q (available: %v)", name, available())
	}
	return factory(), nil
}

func available() []string {
	names := make([]string, 0, len(adapters))
	for k := range adapters {
		names = append(names, k)
	}
	return names
}
