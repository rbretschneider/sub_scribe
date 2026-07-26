package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
)

// existingSource is a representative persisted source for edit tests.
func existingSource() domain.Source {
	return domain.Source{
		ID:              7,
		Name:            "Existing Channel",
		URL:             "https://youtube.com/@existing",
		CollectionType:  domain.CollectionChannel,
		MediaProfileID:  2,
		CookieBehavior:  domain.CookieWhenNeeded,
		IndexFrequency:  24 * time.Hour,
		ShortsRule:      domain.InclusionExclude,
		LivestreamsRule: domain.InclusionInclude,
		Enabled:         true,
	}
}

func TestSourceEditPrefillsForm(t *testing.T) {
	sources := &fakeSources{getResult: existingSource()}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7/edit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Existing Channel", "https://youtube.com/@existing", `action="/sources/7"`, "Edit source", "Save changes"} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %q; body:\n%s", want, body)
		}
	}
}

func TestUpdateSourceValidRedirectsToDetail(t *testing.T) {
	sources := &fakeSources{getResult: existingSource()}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	req := postForm(t, "/sources/7", validSourceForm())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/sources/7" {
		t.Errorf("redirect Location = %q, want /sources/7", got)
	}
	if sources.updateCalls != 1 || sources.updatedID != 7 {
		t.Errorf("UpdateSource calls=%d id=%d, want 1 call for id 7", sources.updateCalls, sources.updatedID)
	}
}

func TestUpdateSourceInvalidReRendersWithoutSaving(t *testing.T) {
	sources := &fakeSources{getResult: existingSource()}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	form := validSourceForm()
	form.Set("collection_type", "bogus") // an invalid enum still rejects the form
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, "/sources/7", form))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-render)", rec.Code)
	}
	if sources.updateCalls != 0 {
		t.Errorf("UpdateSource called %d times on invalid form, want 0", sources.updateCalls)
	}
	if !strings.Contains(rec.Body.String(), `action="/sources/7"`) {
		t.Errorf("re-rendered edit form should keep the edit action; body:\n%s", rec.Body.String())
	}
}

func TestProfilesListsProfiles(t *testing.T) {
	profiles := &fakeProfiles{profiles: []domain.MediaProfile{
		{ID: 1, Name: "1080p Plex", Kind: domain.MediaVideo, OutputPathTemplate: "{{ title }}"},
		{ID: 2, Name: "Podcast Audio", Kind: domain.MediaAudio, OutputPathTemplate: "{{ title }}"},
	}}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "1080p Plex") || !strings.Contains(body, "Podcast Audio") {
		t.Errorf("profiles list missing a profile name; body:\n%s", body)
	}
}

func TestProfileNewShowsDefaults(t *testing.T) {
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/new", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), defaultTemplateHint) {
		t.Errorf("new profile form missing default template; body:\n%s", rec.Body.String())
	}
}

func TestCreateProfileValidRedirectsAndCreatesOnce(t *testing.T) {
	profiles := &fakeProfiles{}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, "/profiles", validProfileForm()))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/profiles" {
		t.Errorf("Location = %q, want /profiles", got)
	}
	if profiles.createCalls != 1 {
		t.Fatalf("CreateProfile calls = %d, want 1", profiles.createCalls)
	}
	if profiles.created.Name != "1080p Plex" || profiles.created.Kind != domain.MediaVideo {
		t.Errorf("created profile = %+v, want name 1080p Plex / video", profiles.created)
	}
	if profiles.created.MetadataFormat != domain.MetadataPlex {
		t.Errorf("created MetadataFormat = %q, want plex", profiles.created.MetadataFormat)
	}
}

func TestCreateProfileBlankNameReRenders(t *testing.T) {
	profiles := &fakeProfiles{}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	form := validProfileForm()
	form.Set("name", "")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, "/profiles", form))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if profiles.createCalls != 0 {
		t.Errorf("CreateProfile called %d times on invalid form, want 0", profiles.createCalls)
	}
}

func TestCreateProfileServiceErrorIsSurfaced(t *testing.T) {
	profiles := &fakeProfiles{createErr: errors.New("unknown template variable \"bogus\"")}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, "/profiles", validProfileForm()))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-render)", rec.Code)
	}
	// The apostrophe in the message is HTML-escaped, so assert on escape-free text.
	body := rec.Body.String()
	if !strings.Contains(body, "save this profile") || !strings.Contains(body, "bogus") {
		t.Errorf("expected surfaced template error; body:\n%s", body)
	}
}

func TestProfileEditPrefillsForm(t *testing.T) {
	profiles := &fakeProfiles{getResult: domain.MediaProfile{
		ID: 9, Name: "My Profile", Kind: domain.MediaVideo, OutputPathTemplate: "{{ source_name }}/{{ title }}",
	}}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/9/edit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"My Profile", `action="/profiles/9"`, "Save changes"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile edit form missing %q; body:\n%s", want, body)
		}
	}
}

func TestUpdateProfilePreservesIdentity(t *testing.T) {
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	profiles := &fakeProfiles{getResult: domain.MediaProfile{ID: 9, Name: "Old", CreatedAt: created}}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, "/profiles/9", validProfileForm()))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if profiles.updateCalls != 1 {
		t.Fatalf("UpdateProfile calls = %d, want 1", profiles.updateCalls)
	}
	if profiles.updated.ID != 9 {
		t.Errorf("updated.ID = %d, want 9 (identity preserved)", profiles.updated.ID)
	}
	if !profiles.updated.CreatedAt.Equal(created) {
		t.Errorf("updated.CreatedAt = %v, want %v (creation time preserved)", profiles.updated.CreatedAt, created)
	}
}

func TestLogsPageShowsRecentEntries(t *testing.T) {
	buf := applog.NewBuffer(10)
	buf.Append(applog.Record{Time: testNow, Level: "ERROR", Message: "download media 5 failed: yt-dlp exploded"})
	server, err := NewServer(ServerDeps{
		Sources: &fakeSources{}, Profiles: &fakeProfiles{}, Library: &fakeLibrary{},
		Logs: buf, CookiesPath: filepath.Join(t.TempDir(), "c.txt"),
		Clock: fixedClock{now: testNow}, EventsPath: "/events",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "yt-dlp exploded") || !strings.Contains(body, "ERROR") {
		t.Errorf("logs page missing the error entry; body:\n%s", body)
	}
}

func TestSourceScanEnqueuesAndRedirects(t *testing.T) {
	sources := &fakeSources{getResult: existingSource()}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sources/7/scan", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/sources/7" {
		t.Errorf("Location = %q, want /sources/7", got)
	}
	if len(sources.scanned) != 1 || sources.scanned[0] != 7 {
		t.Errorf("RequestScan ids = %v, want [7]", sources.scanned)
	}
}

func TestDeleteProfileRedirectsToList(t *testing.T) {
	profiles := &fakeProfiles{}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/profiles/3/delete", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(profiles.deleted) != 1 || profiles.deleted[0] != 3 {
		t.Errorf("deleted ids = %v, want [3]", profiles.deleted)
	}
}

// postForm builds a urlencoded POST request for the given path and values.
func postForm(t *testing.T, path string, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// validProfileForm returns a profile form with all required fields valid.
func validProfileForm() url.Values {
	form := url.Values{}
	form.Set("name", "1080p Plex")
	form.Set("output_path_template", "{{ source_name }}/{{ title }}")
	form.Set("kind", "video")
	form.Set("quality_format", "bestvideo+bestaudio")
	form.Set("metadata_format", "plex")
	form.Set("sponsorblock_mode", "off")
	form.Set("redownload_days", "0")
	return form
}
