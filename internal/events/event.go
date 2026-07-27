// Package events carries lightweight application events from background work to
// the live UI. Services publish through the Publisher interface; the web layer's
// SSE hub implements it and streams to browsers, so the two sides stay decoupled.
package events

// Kind classifies a UI event so the frontend can decide how to react.
type Kind string

const (
	// KindSourceIndexed fires when a source finishes indexing.
	KindSourceIndexed Kind = "source_indexed"
	// KindMediaProgress fires as a media item downloads.
	KindMediaProgress Kind = "media_progress"
	// KindMediaCompleted fires when a media item finishes downloading.
	KindMediaCompleted Kind = "media_completed"
	// KindMediaFailed fires when a media download fails.
	KindMediaFailed Kind = "media_failed"
	// KindTokenChanged fires when the cookie/token health changes.
	KindTokenChanged Kind = "token_changed"
)

// Event is a single UI notification. Fields are optional and interpreted per
// Kind; Percent is meaningful only for progress events. Events carry no secrets.
// The JSON names are part of the contract with the browser script, so they are
// spelled explicitly rather than left to Go's exported-field defaults — which
// would emit "SourceID" while the script looks for "source", and silently never
// match.
type Event struct {
	Kind     Kind    `json:"kind"`
	SourceID int64   `json:"source"`
	MediaID  int64   `json:"media"`
	Title    string  `json:"title,omitempty"`
	Message  string  `json:"message,omitempty"`
	Percent  float64 `json:"percent"`
}

// Publisher accepts events for delivery to connected clients. Publish must be
// non-blocking and safe for concurrent use; a slow or absent consumer must never
// stall the caller that produced the event.
type Publisher interface {
	Publish(event Event)
}

// NopPublisher is a Publisher that discards events, for tests and headless runs.
type NopPublisher struct{}

// Publish does nothing.
func (NopPublisher) Publish(Event) {}
