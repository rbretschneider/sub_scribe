package web

import (
	"strings"
	"testing"

	"sub_scribe/internal/events"
)

// TestBrowserScriptListensForEveryEventKind pins the one contract that has no
// compiler to enforce it: the SSE frame names the server writes and the names
// app.js subscribes to.
//
// A named SSE frame never falls through to onmessage, so a listener registered
// under a name the server does not emit hears nothing — silently, forever. That
// is precisely what happened to download progress: the server sent
// "media_progress" and the script listened for "progress", so every progress
// bar on the dashboard stayed frozen while downloads ran normally.
func TestBrowserScriptListensForEveryEventKind(t *testing.T) {
	script, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	text := string(script)

	kinds := []events.Kind{
		events.KindSourceIndexed,
		events.KindMediaProgress,
		events.KindMediaCompleted,
		events.KindMediaFailed,
		events.KindTokenChanged,
	}
	for _, kind := range kinds {
		if !strings.Contains(text, `"`+string(kind)+`"`) {
			t.Errorf("app.js never mentions event kind %q, so those frames are dropped", kind)
		}
	}

	// The invented names this bug was made of must not come back.
	for _, bogus := range []string{`addEventListener("progress"`, `addEventListener("activity"`} {
		if strings.Contains(text, bogus) {
			t.Errorf("app.js listens for %s, a name the server never emits", bogus)
		}
	}
}
