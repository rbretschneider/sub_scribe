package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

func TestDownloadFormOffersTheURLField(t *testing.T) {
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads/new", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="url"`) || !strings.Contains(body, `action="/downloads"`) {
		t.Error("form is missing the url field or its action")
	}
}

func TestDownloadCreateQueuesAndLandsOnTheVideo(t *testing.T) {
	sources := &fakeSources{downloadID: 42}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	form := url.Values{"url": {"https://youtu.be/gCZOjDar1tU"}}
	rec := submitForm(t, server, "/downloads", form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/library/42" {
		t.Errorf("Location = %q, want /library/42 to watch it download", got)
	}
	if len(sources.downloadedURLs) != 1 || sources.downloadedURLs[0] != "https://youtu.be/gCZOjDar1tU" {
		t.Errorf("downloadedURLs = %v", sources.downloadedURLs)
	}
}

func TestDownloadCreateRoutesTheChosenProfile(t *testing.T) {
	sources := &fakeSources{downloadID: 42}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	form := url.Values{"url": {"https://youtu.be/gCZOjDar1tU"}, "profile_id": {"3"}}
	rec := submitForm(t, server, "/downloads", form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if sources.downloadProfileID != 3 {
		t.Errorf("profile id = %d, want the chosen 3", sources.downloadProfileID)
	}
}

func TestDownloadFormOffersAProfilePickerWhenThereAreChoices(t *testing.T) {
	profiles := &fakeProfiles{profiles: []domain.MediaProfile{
		{ID: 1, Name: "Default (1080p, Plex layout)"},
		{ID: 2, Name: "Movies (Plex)", DownloadDir: "/movies"},
	}}
	server := newTestServer(t, &fakeSources{}, profiles, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads/new", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `name="profile_id"`) {
		t.Error("with several profiles the form should offer the Save as picker")
	}
	// The picker shows where a routed profile files things, so the choice is
	// informed rather than a bare name.
	if !strings.Contains(body, "/movies") {
		t.Error("a profile with its own folder should show that destination")
	}
}

func TestDownloadCreateExplainsANonVideoLink(t *testing.T) {
	sources := &fakeSources{downloadErr: fmt.Errorf("%w: nope", library.ErrNotAVideoURL)}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	form := url.Values{"url": {"https://www.youtube.com/@SomeChannel"}}
	rec := submitForm(t, server, "/downloads", form)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form re-rendered)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "single video") {
		t.Error("the error should explain what kind of link is expected")
	}
	if !strings.Contains(body, "youtube.com/@SomeChannel") {
		t.Error("the pasted text should be preserved for correction")
	}
}
