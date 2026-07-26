package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// fixedClock is a deterministic Clock so token expiry assessments are stable.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// testNow anchors every test's clock; auth cookies are dated relative to it.
var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// fakeSources is an in-memory SourceService that records calls, so tests assert
// the handler's contract (what it invokes) without a real database.
type fakeSources struct {
	sources     []domain.Source
	addCalls    int
	added       library.AddSourceInput
	addErr      error
	getResult   domain.Source
	getErr      error
	deleted     []int64
	scanned     []int64
	updateCalls int
	updatedID   int64
	updated     library.AddSourceInput
	updateErr   error

	enabledCalls []enabledCall
	enabledErr   error

	// deletedWithFiles records, per delete call, whether removing the media files
	// was requested too — the thing that must never default to true.
	deletedWithFiles []bool
}

func (f *fakeSources) AddSource(_ context.Context, input library.AddSourceInput) (domain.Source, error) {
	f.addCalls++
	f.added = input
	if f.addErr != nil {
		return domain.Source{}, f.addErr
	}
	return domain.Source{ID: 1, Name: input.Name}, nil
}

func (f *fakeSources) GetSource(_ context.Context, id int64) (domain.Source, error) {
	return f.getResult, f.getErr
}

func (f *fakeSources) ListSources(_ context.Context) ([]domain.Source, error) {
	return f.sources, nil
}

func (f *fakeSources) UpdateSource(_ context.Context, id int64, input library.AddSourceInput) (domain.Source, error) {
	f.updateCalls++
	f.updatedID = id
	f.updated = input
	if f.updateErr != nil {
		return domain.Source{}, f.updateErr
	}
	return domain.Source{ID: id, Name: input.Name}, nil
}

func (f *fakeSources) DeleteSource(_ context.Context, id int64, opts library.DeleteSourceOptions) error {
	f.deleted = append(f.deleted, id)
	f.deletedWithFiles = append(f.deletedWithFiles, opts.DeleteFiles)
	return nil
}

func (f *fakeSources) RequestScan(_ context.Context, id int64) error {
	f.scanned = append(f.scanned, id)
	return nil
}

func (f *fakeSources) SetSourceEnabled(_ context.Context, id int64, enabled bool) error {
	if f.enabledErr != nil {
		return f.enabledErr
	}
	f.enabledCalls = append(f.enabledCalls, enabledCall{id: id, enabled: enabled})
	return nil
}

// enabledCall records one pause/resume request so tests can assert the desired
// state was submitted, not merely that the handler was reached.
type enabledCall struct {
	id      int64
	enabled bool
}

// fakeProfiles is an in-memory ProfileService that records calls so profile
// handler tests assert on the contract without a real database.
type fakeProfiles struct {
	profiles    []domain.MediaProfile
	getResult   domain.MediaProfile
	getErr      error
	created     domain.MediaProfile
	createCalls int
	createErr   error
	updated     domain.MediaProfile
	updateCalls int
	updateErr   error
	deleted     []int64
}

func (f *fakeProfiles) CreateProfile(_ context.Context, p domain.MediaProfile) (domain.MediaProfile, error) {
	f.createCalls++
	f.created = p
	if f.createErr != nil {
		return domain.MediaProfile{}, f.createErr
	}
	p.ID = 1
	return p, nil
}
func (f *fakeProfiles) GetProfile(_ context.Context, id int64) (domain.MediaProfile, error) {
	return f.getResult, f.getErr
}
func (f *fakeProfiles) ListProfiles(_ context.Context) ([]domain.MediaProfile, error) {
	return f.profiles, nil
}
func (f *fakeProfiles) UpdateProfile(_ context.Context, p domain.MediaProfile) error {
	f.updateCalls++
	f.updated = p
	return f.updateErr
}
func (f *fakeProfiles) DeleteProfile(_ context.Context, id int64) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeLibrary is an in-memory LibraryReader returning canned read views.
type fakeLibrary struct {
	overview library.Overview
	media    []library.MediaListItem
	// item is returned by GetMedia; getErr overrides it to exercise the
	// not-found and failure paths of the detail screen.
	item   library.MediaListItem
	getErr error
	stats  map[int64]library.SourceStats
}

func (f *fakeLibrary) Overview(context.Context) (library.Overview, error) {
	return f.overview, nil
}
func (f *fakeLibrary) ListMedia(context.Context, domain.MediaStatus, int) ([]library.MediaListItem, error) {
	return f.media, nil
}
func (f *fakeLibrary) SourceStats(context.Context) (map[int64]library.SourceStats, error) {
	return f.stats, nil
}
func (f *fakeLibrary) GetMedia(context.Context, int64) (library.MediaListItem, error) {
	if f.getErr != nil {
		return library.MediaListItem{}, f.getErr
	}
	return f.item, nil
}

// newTestServer builds a Server with the given fakes, a default empty library,
// and a temp cookies path.
func newTestServer(t *testing.T, sources *fakeSources, profiles *fakeProfiles, cookiesPath string) *Server {
	return newTestServerFull(t, sources, profiles, &fakeLibrary{}, cookiesPath)
}

// newTestServerFull builds a Server, letting a test supply a specific library.
func newTestServerFull(t *testing.T, sources *fakeSources, profiles *fakeProfiles, lib library.LibraryReader, cookiesPath string) *Server {
	t.Helper()
	server, err := NewServer(ServerDeps{
		Sources:     sources,
		Profiles:    profiles,
		Library:     lib,
		Logs:        applog.NewBuffer(0),
		CookiesPath: cookiesPath,
		Clock:       fixedClock{now: testNow},
		EventsPath:  "/events",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func TestDashboardShowsOverviewAndTokenBadge(t *testing.T) {
	lib := &fakeLibrary{overview: library.Overview{
		SourceCount: 3,
		Counts:      map[domain.MediaStatus]int{domain.MediaDownloaded: 42},
	}}
	server := newTestServerFull(t, &fakeSources{}, &fakeProfiles{}, lib, filepath.Join(t.TempDir(), "missing.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Your archive") || !strings.Contains(body, "42") {
		t.Errorf("dashboard missing overview content; body:\n%s", body)
	}
	if !strings.Contains(body, "Not connected") {
		t.Errorf("dashboard missing token badge text; body:\n%s", body)
	}
}

func TestSourcesPageListsSources(t *testing.T) {
	sources := &fakeSources{sources: []domain.Source{
		{ID: 7, Name: "Cool Channel", URL: "https://youtube.com/@cool", CollectionType: domain.CollectionChannel, Enabled: true, IndexFrequency: 6 * time.Hour},
	}}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Cool Channel") {
		t.Errorf("sources page missing source name; body:\n%s", rec.Body.String())
	}
}

func TestLibraryShowsArchivedMedia(t *testing.T) {
	lib := &fakeLibrary{
		overview: library.Overview{TotalMedia: 1},
		media: []library.MediaListItem{{
			Media:      domain.Media{ExternalID: "abc", Status: domain.MediaDownloaded, Metadata: domain.MediaMetadata{Title: "Cool Video"}},
			SourceName: "Cool Channel",
		}},
	}
	server := newTestServerFull(t, &fakeSources{}, &fakeProfiles{}, lib, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cool Video") || !strings.Contains(body, "Cool Channel") {
		t.Errorf("library missing archived media; body:\n%s", body)
	}
}

func TestSourceNewShowsProfileOptions(t *testing.T) {
	profiles := &fakeProfiles{profiles: []domain.MediaProfile{
		{ID: 3, Name: "1080p Plex"},
		{ID: 4, Name: "Audio only"},
	}}
	server := newTestServer(t, &fakeSources{}, profiles, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/new", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"1080p Plex", "Audio only", `value="3"`, `value="4"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q; body:\n%s", want, body)
		}
	}
}

func TestCreateSourceValidRedirectsAndAddsOnce(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	form := validSourceForm()
	req := httptest.NewRequest(http.MethodPost, "/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("redirect Location = %q, want /", got)
	}
	if sources.addCalls != 1 {
		t.Fatalf("AddSource called %d times, want 1", sources.addCalls)
	}
	if sources.added.Name != "My Channel" {
		t.Errorf("added.Name = %q, want My Channel", sources.added.Name)
	}
	if sources.added.IndexFrequency != 24*time.Hour {
		t.Errorf("added.IndexFrequency = %v, want 24h", sources.added.IndexFrequency)
	}
	if sources.added.ShortsRule != domain.InclusionExclude {
		t.Errorf("added.ShortsRule = %q, want exclude", sources.added.ShortsRule)
	}
}

func TestCreateSourceCutoffWindowPreset(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	form := validSourceForm()
	form.Set("cutoff_window", "365") // "the last year"
	req := httptest.NewRequest(http.MethodPost, "/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if sources.added.CutoffWindow != 365*24*time.Hour {
		t.Errorf("CutoffWindow = %v, want 365 days", sources.added.CutoffWindow)
	}
	if sources.added.DownloadCutoff != nil {
		t.Errorf("DownloadCutoff = %v, want nil for a rolling window", sources.added.DownloadCutoff)
	}
}

func TestCreateSourceCutoffCustomDate(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	form := validSourceForm()
	form.Set("cutoff_window", "custom")
	form.Set("download_cutoff", "2025-06-01")
	req := httptest.NewRequest(http.MethodPost, "/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if sources.added.CutoffWindow != 0 {
		t.Errorf("CutoffWindow = %v, want 0 for a custom date", sources.added.CutoffWindow)
	}
	if sources.added.DownloadCutoff == nil {
		t.Fatal("DownloadCutoff = nil, want the custom date")
	}
}

func TestCreateSourceBlankNameIsAccepted(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	form := validSourceForm()
	form.Set("name", "") // blank name is now allowed — filled from the channel on index
	req := httptest.NewRequest(http.MethodPost, "/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (accepted)", rec.Code)
	}
	if sources.addCalls != 1 {
		t.Errorf("AddSource called %d times, want 1", sources.addCalls)
	}
	if sources.added.Name != "" {
		t.Errorf("added.Name = %q, want empty (to be auto-filled)", sources.added.Name)
	}
}

func TestTokenUploadValidWritesFileAndShowsConnected(t *testing.T) {
	cookiesPath := filepath.Join(t.TempDir(), "cookies.txt")
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, cookiesPath)

	rec := postCookies(t, server, loggedInCookies())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Connected") {
		t.Errorf("expected connected message; body:\n%s", rec.Body.String())
	}
	written, err := os.ReadFile(cookiesPath)
	if err != nil {
		t.Fatalf("cookies file not written: %v", err)
	}
	if !strings.Contains(string(written), "SID") {
		t.Errorf("written cookies missing SID: %q", written)
	}
}

func TestTokenUploadLoggedOutShowsErrorAndKeepsExistingFile(t *testing.T) {
	cookiesPath := filepath.Join(t.TempDir(), "cookies.txt")
	good := loggedInCookies()
	if err := os.WriteFile(cookiesPath, []byte(good), cookiesFileMode); err != nil {
		t.Fatalf("seeding good file: %v", err)
	}
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, cookiesPath)

	rec := postCookies(t, server, loggedOutCookies())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no YouTube login") {
		t.Errorf("expected friendly no-login error; body:\n%s", rec.Body.String())
	}
	after, err := os.ReadFile(cookiesPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(after) != good {
		t.Errorf("existing good file was overwritten; got %q", after)
	}
}

func TestSourceDetailNotFoundForMissing(t *testing.T) {
	sources := &fakeSources{getErr: errors.New("not found")}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/99", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteSourceRedirectsHome(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, filepath.Join(t.TempDir(), "c.txt"))

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sources/5/delete", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(sources.deleted) != 1 || sources.deleted[0] != 5 {
		t.Errorf("DeleteSource ids = %v, want [5]", sources.deleted)
	}
}

// validSourceForm returns a form with every required field filled correctly.
func validSourceForm() url.Values {
	form := url.Values{}
	form.Set("name", "My Channel")
	form.Set("url", "https://youtube.com/@me")
	form.Set("collection_type", "channel")
	form.Set("media_profile_id", "2")
	form.Set("cookie_behavior", "when_needed")
	form.Set("frequency_hours", "24")
	form.Set("shorts_rule", "exclude")
	form.Set("livestreams_rule", "include")
	return form
}

// postCookies uploads a cookies.txt body through the multipart token endpoint.
func postCookies(t *testing.T, server *Server, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(cookieFormField, "cookies.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("writing part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/token", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

// loggedInCookies is a cookies.txt with an SID auth cookie expiring well after
// the fixed clock, so it assesses as healthy.
func loggedInCookies() string {
	expiry := testNow.Add(60 * 24 * time.Hour).Unix()
	return fmt.Sprintf(".youtube.com\tTRUE\t/\tTRUE\t%d\tSID\tsecretvalue\n", expiry)
}

// loggedOutCookies is a valid cookies.txt with no YouTube auth cookie.
func loggedOutCookies() string {
	expiry := testNow.Add(60 * 24 * time.Hour).Unix()
	return fmt.Sprintf(".youtube.com\tTRUE\t/\tTRUE\t%d\tPREF\tf6%%3D0\n", expiry)
}
