package events

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// subscriberBufferSize bounds each subscriber's pending-event queue. A slow
// client that falls behind by more than this many events has its excess dropped
// rather than stalling producers.
const subscriberBufferSize = 16

// heartbeatInterval is how often an idle SSE stream sends a comment line. This
// keeps proxies from closing an idle connection and, more importantly, makes the
// server write to the socket periodically so a client that has gone away is
// detected (via write failure) and its connection promptly reaped.
const heartbeatInterval = 15 * time.Second

// Hub is a concurrency-safe fan-out of events to connected SSE clients. It
// implements Publisher for producers and http.Handler for browsers. A slow or
// disconnected client never blocks a publisher: sends are non-blocking and
// overflowing events are dropped for that client only.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
}

// NewHub returns a ready-to-use Hub with no subscribers.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan []byte]struct{})}
}

// Publish marshals the event to JSON and delivers it to every subscriber with a
// non-blocking send, dropping the event for any subscriber whose buffer is full.
// It is safe for concurrent use and never blocks the caller.
func (h *Hub) Publish(event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subscribers {
		select {
		case sub <- payload:
		default:
		}
	}
}

// ServeHTTP streams events to a browser as Server-Sent Events until the request
// context is cancelled or streaming is unsupported by the ResponseWriter.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	writeStreamHeaders(w)
	flusher.Flush()

	sub := h.subscribe()
	defer h.unsubscribe(sub)

	h.stream(w, r, flusher, sub)
}

// stream pumps buffered payloads to the client until the context is cancelled,
// emitting a heartbeat comment during idle periods so dead connections are
// noticed and closed rather than held open.
func (h *Hub) stream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, sub chan []byte) {
	done := r.Context().Done()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-done:
			return
		case <-heartbeat.C:
			if !writeComment(w) {
				return
			}
			flusher.Flush()
		case payload := <-sub:
			if !writeEvent(w, payload) {
				return
			}
			flusher.Flush()
		}
	}
}

// subscribe registers and returns a new buffered subscriber channel.
func (h *Hub) subscribe() chan []byte {
	sub := make(chan []byte, subscriberBufferSize)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers[sub] = struct{}{}
	return sub
}

// unsubscribe removes a subscriber so it no longer receives events.
func (h *Hub) unsubscribe(sub chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, sub)
}

// writeStreamHeaders sets the headers required for an SSE response.
func writeStreamHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
}

// writeComment writes an SSE comment line (ignored by clients), used as a
// heartbeat to keep the connection alive and detect a departed client.
func writeComment(w http.ResponseWriter) bool {
	_, err := w.Write([]byte(": ping\n\n"))
	return err == nil
}

// writeEvent writes one SSE "data:" frame, reporting whether the write succeeded.
func writeEvent(w http.ResponseWriter, payload []byte) bool {
	if _, err := w.Write([]byte("data: ")); err != nil {
		return false
	}
	if _, err := w.Write(payload); err != nil {
		return false
	}
	_, err := w.Write([]byte("\n\n"))
	return err == nil
}

var _ Publisher = (*Hub)(nil)
var _ http.Handler = (*Hub)(nil)
