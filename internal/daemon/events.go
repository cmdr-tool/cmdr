package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// Event represents a server-sent event.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// EventBus manages SSE client connections and broadcasting.
type EventBus struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan Event]struct{}),
	}
}

func (eb *EventBus) Subscribe() chan Event {
	ch := make(chan Event, 16)
	eb.mu.Lock()
	eb.clients[ch] = struct{}{}
	eb.mu.Unlock()
	return ch
}

func (eb *EventBus) Unsubscribe(ch chan Event) {
	eb.mu.Lock()
	delete(eb.clients, ch)
	close(ch)
	eb.mu.Unlock()
}

func (eb *EventBus) Publish(e Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	for ch := range eb.clients {
		select {
		case ch <- e:
		default:
			// slow client, drop event
		}
	}
}

func handleEvents(bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher.Flush() // Flush headers immediately so the browser fires 'open'

		ch := bus.Subscribe()
		defer bus.Unsubscribe(ch)

		// Keep connection alive until client disconnects
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(evt.Data)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
				flusher.Flush()
			}
		}
	}
}
