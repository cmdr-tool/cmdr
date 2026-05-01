package graphtrace

import (
	"sync"
)

// Phase tags the high-level stage of a generation run.
type Phase string

const (
	PhaseGenerating Phase = "generating"
	PhaseComparing  Phase = "comparing"
	PhaseDone       Phase = "done"
	PhaseFailed     Phase = "failed"
)

// Event is one unit of progress published while a trace is being generated.
// Subscribers consume these via Subscribe().
type Event struct {
	Type   string `json:"type"`             // "phase" | "tool" | "text" | "error"
	Phase  Phase  `json:"phase,omitempty"`  // for Type=="phase"
	Tool   string `json:"tool,omitempty"`   // for Type=="tool"
	Detail string `json:"detail,omitempty"` // for Type=="tool"
	Text   string `json:"text,omitempty"`   // for Type=="text" or "error"
}

// channels keeps one slice of subscriber chans per trace id. We hold
// onto closed channels until the publisher signals completion via
// CloseChannel — callers that subscribe after a run finishes will see
// a closed channel and immediately observe end-of-stream, which is the
// behavior the SSE handler wants when the user opens the page after a
// run has already wrapped.
type pubsub struct {
	mu      sync.Mutex
	subs    map[int64][]chan Event
	closed  map[int64]bool
	pending map[int64][]Event // events buffered before any subscriber arrives, replayed on subscribe
}

var bus = &pubsub{
	subs:    map[int64][]chan Event{},
	closed:  map[int64]bool{},
	pending: map[int64][]Event{},
}

// Publish broadcasts an event to all current subscribers of a trace id
// and buffers it for late subscribers (replayed on Subscribe).
//
// Buffering is bounded — the most recent 200 events per trace id are
// kept. Generation runs emit on the order of a few dozen events, so
// this comfortably covers the full run for a late subscriber while
// guarding against pathological streams.
func Publish(traceID int64, e Event) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed[traceID] {
		return
	}

	const maxBuffered = 200
	buf := bus.pending[traceID]
	buf = append(buf, e)
	if len(buf) > maxBuffered {
		buf = buf[len(buf)-maxBuffered:]
	}
	bus.pending[traceID] = buf

	for _, ch := range bus.subs[traceID] {
		select {
		case ch <- e:
		default:
			// Subscriber's channel is full — drop the event for that
			// subscriber. The buffer above still has it for replay.
		}
	}
}

// Subscribe returns a channel that receives events for the trace id.
// The returned cleanup function must be called when the subscriber
// is done reading. If the trace's stream has already been closed,
// Subscribe replays buffered events and then closes the channel.
func Subscribe(traceID int64) (<-chan Event, func()) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	ch := make(chan Event, 64)

	// Replay buffered events synchronously into the buffered channel.
	// 64 is enough headroom for the maxBuffered cap above + a few new
	// events arriving before the subscriber drains.
	for _, e := range bus.pending[traceID] {
		select {
		case ch <- e:
		default:
		}
	}

	if bus.closed[traceID] {
		close(ch)
		return ch, func() {}
	}

	bus.subs[traceID] = append(bus.subs[traceID], ch)

	cleanup := func() {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		subs := bus.subs[traceID]
		for i, c := range subs {
			if c == ch {
				bus.subs[traceID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		// Drain to avoid leaking pending sends if Publish is mid-call.
		go func() {
			for range ch {
			}
		}()
		// Use safeClose because Close(traceID) may have already closed
		// this channel — the cleanup func and the publisher's Close
		// path both target the same channels and either can run first.
		safeClose(ch)
	}
	return ch, cleanup
}

// Close marks the trace's stream as terminated. Closes any active
// subscriber channels and forgets the buffer so the next run on the
// same id starts fresh.
func Close(traceID int64) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.closed[traceID] = true
	for _, ch := range bus.subs[traceID] {
		// Best-effort close; the cleanup func returned by Subscribe
		// also closes the chan, but only the first close matters and
		// duplicate closes panic. Use a recover-guarded helper.
		safeClose(ch)
	}
	delete(bus.subs, traceID)
	delete(bus.pending, traceID)
}

// Reset clears any prior closed-state and buffer for a trace id, so
// a subsequent run (e.g. regenerate) starts a fresh stream. Safe to
// call before Publish'ing the first event of a new run.
func Reset(traceID int64) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	delete(bus.closed, traceID)
	delete(bus.pending, traceID)
}

func safeClose(ch chan Event) {
	defer func() { recover() }()
	close(ch)
}
