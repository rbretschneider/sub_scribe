package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sub_scribe/internal/applog"
)

// feedServer builds a Server whose feed directory is the given path.
func feedServer(t *testing.T, feedDir string) *Server {
	t.Helper()
	server, err := NewServer(ServerDeps{
		Sources:     &fakeSources{},
		Profiles:    &fakeProfiles{},
		Library:     &fakeLibrary{},
		Jobs:        &fakeJobs{},
		Queue:       &fakeJobs{},
		Logs:        applog.NewBuffer(0),
		CookiesPath: filepath.Join(t.TempDir(), "cookies.txt"),
		FeedDir:     feedDir,
		Clock:       fixedClock{now: testNow},
		EventsPath:  "/events",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func TestFeedIsServedAsRSS(t *testing.T) {
	feedDir := t.TempDir()
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Chan</title></channel></rss>`
	if err := os.WriteFile(filepath.Join(feedDir, "7.xml"), []byte(rss), 0o644); err != nil {
		t.Fatalf("seed feed: %v", err)
	}

	rec := httptest.NewRecorder()
	feedServer(t, feedDir).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feeds/7", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != feedContentType {
		t.Errorf("Content-Type = %q, want %q", got, feedContentType)
	}
	if !strings.Contains(rec.Body.String(), "<rss") {
		t.Errorf("body is not the feed: %q", rec.Body.String())
	}
}

func TestFeedNotYetWrittenExplainsItself(t *testing.T) {
	rec := httptest.NewRecorder()
	feedServer(t, t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feeds/9", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "first download") {
		t.Errorf("404 body should say when the feed will appear, got %q", rec.Body.String())
	}
}

func TestFeedRejectsANonNumericId(t *testing.T) {
	// The id is formatted into the filename, so anything non-numeric — including
	// a traversal attempt — must be rejected before touching the filesystem.
	rec := httptest.NewRecorder()
	feedServer(t, t.TempDir()).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/feeds/..%2fcookies.txt", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("a non-numeric feed id must not serve a file")
	}
}
