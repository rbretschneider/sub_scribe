package events

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recvTimeout bounds how long a test waits for an expected event before failing.
const recvTimeout = time.Second

func TestPublishWithNoSubscribersDoesNotBlock(t *testing.T) {
	hub := NewHub()

	done := make(chan struct{})
	go func() {
		hub.Publish(Event{Kind: KindSourceIndexed, SourceID: 1})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(recvTimeout):
		t.Fatal("Publish blocked with zero subscribers")
	}
}

func TestSubscriberReceivesPublishedEventJSON(t *testing.T) {
	hub := NewHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	want := Event{Kind: KindMediaProgress, MediaID: 7, Title: "clip", Percent: 42.5}
	hub.Publish(want)

	got := receiveEvent(t, sub)
	if got != want {
		t.Fatalf("received %+v, want %+v", got, want)
	}
}

func TestUnsubscribedChannelReceivesNothing(t *testing.T) {
	hub := NewHub()
	sub := hub.subscribe()
	hub.unsubscribe(sub)

	hub.Publish(Event{Kind: KindTokenChanged})

	select {
	case payload, ok := <-sub:
		if ok {
			t.Fatalf("unsubscribed channel received %q", payload)
		}
	default:
	}
}

func TestPublishDropsWhenSubscriberBufferFull(t *testing.T) {
	hub := NewHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	total := subscriberBufferSize + 5
	for i := 0; i < total; i++ {
		hub.Publish(Event{Kind: KindMediaProgress, MediaID: int64(i)})
	}

	if len(sub) != subscriberBufferSize {
		t.Fatalf("buffered %d events, want %d", len(sub), subscriberBufferSize)
	}
}

func TestServeHTTPStreamsEventAndStopsOnCancel(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	served := make(chan struct{})
	go func() {
		hub.ServeHTTP(rec, req)
		close(served)
	}()

	waitForSubscriber(t, hub)
	hub.Publish(Event{Kind: KindMediaCompleted, MediaID: 3})

	cancel()
	select {
	case <-served:
	case <-time.After(recvTimeout):
		t.Fatal("ServeHTTP did not return after context cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: ") || !strings.Contains(body, "media_completed") {
		t.Fatalf("response body missing SSE event frame: %q", body)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

// receiveEvent reads one payload from sub and unmarshals it, failing on timeout.
func receiveEvent(t *testing.T, sub chan []byte) Event {
	t.Helper()
	select {
	case payload := <-sub:
		var got Event
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		return got
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// waitForSubscriber blocks until the hub has registered at least one subscriber.
func waitForSubscriber(t *testing.T, hub *Hub) {
	t.Helper()
	deadline := time.After(recvTimeout)
	for {
		hub.mu.Lock()
		count := len(hub.subscribers)
		hub.mu.Unlock()
		if count > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no subscriber registered")
		case <-time.After(time.Millisecond):
		}
	}
}
