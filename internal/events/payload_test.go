package events

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEventPayloadKeysMatchTheBrowserContract pins the wire format the browser
// script reads. Go's default marshalling would emit "SourceID"/"MediaID", the
// script looks for "source"/"media", and the mismatch is silent: every progress
// event arrives, parses, and is discarded, so the download panel simply never
// updates.
func TestEventPayloadKeysMatchTheBrowserContract(t *testing.T) {
	payload, err := json.Marshal(Event{
		Kind: KindMediaProgress, SourceID: 7, MediaID: 42, Percent: 55.5,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for key, want := range map[string]any{
		"kind": string(KindMediaProgress), "source": 7.0, "media": 42.0, "percent": 55.5,
	} {
		got, ok := decoded[key]
		if !ok {
			t.Errorf("payload has no %q key: %s", key, payload)
			continue
		}
		if got != want {
			t.Errorf("%q = %v, want %v", key, got, want)
		}
	}
}

// TestFrameNamesTheEventKind pins the "event:" line. Without it every frame
// arrives as the default "message" type, and a client subscribing by name with
// addEventListener never hears anything at all.
func TestFrameNamesTheEventKind(t *testing.T) {
	var frame strings.Builder
	payload, err := json.Marshal(Event{Kind: KindMediaProgress, MediaID: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !writeEvent(&frame, payload) {
		t.Fatal("writeEvent reported failure")
	}

	text := frame.String()
	if !strings.HasPrefix(text, "event: media_progress\n") {
		t.Errorf("frame does not name its kind:\n%s", text)
	}
	if !strings.Contains(text, "data: {") || !strings.HasSuffix(text, "\n\n") {
		t.Errorf("frame is not a well-formed SSE message:\n%s", text)
	}
}
