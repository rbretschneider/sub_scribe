package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// submitForm posts form values to path and returns the recorded response.
func submitForm(t *testing.T, server *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, postForm(t, path, form))
	return rec
}

// enabledForm builds the pause/resume form body, optionally with a return path.
func enabledForm(enabled bool, returnTo string) url.Values {
	form := url.Values{}
	form.Set("enabled", strconv.FormatBool(enabled))
	if returnTo != "" {
		form.Set("return_to", returnTo)
	}
	return form
}

func TestSourcesListOffersPauseOrResumePerSource(t *testing.T) {
	sources := &fakeSources{sources: []domain.Source{
		{ID: 1, Name: "Active Channel", Enabled: true, CollectionType: domain.CollectionChannel},
		{ID: 2, Name: "Paused Channel", Enabled: false, CollectionType: domain.CollectionChannel},
	}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `action="/sources/1/enabled"`) {
		t.Error("no pause control for the active source")
	}
	// The control submits the state it wants, so a stale page cannot invert it.
	if !strings.Contains(body, `value="false"`) {
		t.Error("the active source's control should request enabled=false")
	}
	if !strings.Contains(body, "Pause") || !strings.Contains(body, "Resume") {
		t.Errorf("expected both a Pause and a Resume control, got:\n%s", body)
	}
}

func TestSetSourceEnabledSubmitsTheRequestedState(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"pause", false},
		{"resume", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := &fakeSources{}
			server := newTestServer(t, sources, &fakeProfiles{}, "")

			rec := submitForm(t, server, "/sources/7/enabled", enabledForm(test.want, ""))

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			if len(sources.enabledCalls) != 1 {
				t.Fatalf("recorded %d calls, want 1", len(sources.enabledCalls))
			}
			call := sources.enabledCalls[0]
			if call.id != 7 || call.enabled != test.want {
				t.Errorf("call = %+v, want id 7 enabled %v", call, test.want)
			}
		})
	}
}

func TestSetSourceEnabledReturnsToThePageItWasCalledFrom(t *testing.T) {
	tests := []struct {
		name     string
		returnTo string
		want     string
	}{
		{"from the list", "/sources", "/sources"},
		{"from the detail page", "", "/sources/7"},
		{"an off-site return is ignored", "https://evil.test", "/sources/7"},
		{"a protocol-relative return is ignored", "//evil.test", "/sources/7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, "")

			rec := submitForm(t, server, "/sources/7/enabled", enabledForm(false, test.returnTo))

			if got := rec.Header().Get("Location"); got != test.want {
				t.Errorf("Location = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRetentionAcceptsAPresetOrACustomDayCount(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		days    string
		wantDur time.Duration
	}{
		{"a preset period", "90", "", 90 * 24 * time.Hour},
		{"custom takes the typed value", "custom", "45", 45 * 24 * time.Hour},
		{"empty keeps everything", "", "", 0},
		// A stale number left in the hidden box must not override the dropdown.
		{"preset wins over a leftover number", "30", "999", 30 * 24 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := sourceFormValues{
				Name: "C", URL: "https://youtube.com/@c", CollectionType: "channel",
				MediaProfileID: "1", CookieBehavior: "when_needed", FrequencyHours: "6",
				ShortsRule: "include", LivestreamsRule: "include",
				RetentionPreset: test.preset, RetentionDays: test.days,
			}

			input, err := values.toInput()
			if err != nil {
				t.Fatalf("toInput: %v", err)
			}
			if input.RetentionAfter != test.wantDur {
				t.Errorf("RetentionAfter = %v, want %v", input.RetentionAfter, test.wantDur)
			}
		})
	}
}

func TestRetentionDropdownPreselectsAStoredPeriod(t *testing.T) {
	tests := []struct {
		name      string
		retention time.Duration
		want      string
	}{
		{"an offered period selects itself", 90 * 24 * time.Hour, "90"},
		{"an unusual period falls back to custom", 45 * 24 * time.Hour, "custom"},
		{"no retention selects never", 0, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fromSource(domain.Source{RetentionAfter: test.retention})

			if got.RetentionPreset != test.want {
				t.Errorf("RetentionPreset = %q, want %q", got.RetentionPreset, test.want)
			}
		})
	}
}

func TestSourceFormOffersRetentionShortcuts(t *testing.T) {
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/new", nil))
	body := rec.Body.String()

	for _, want := range []string{`name="retention_preset"`, ">30 days<", ">60 days<", ">90 days<"} {
		if !strings.Contains(body, want) {
			t.Errorf("source form missing retention shortcut %q", want)
		}
	}
}

func TestSourceDetailExplainsAPausedSource(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{
		ID: 7, Name: "Paused Channel", Enabled: false,
		CollectionType: domain.CollectionChannel,
	}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "Resume scanning") {
		t.Error("a paused source should offer to resume")
	}
	if !strings.Contains(body, "will not be scanned") {
		t.Error("a paused source should say what being paused means")
	}
}

// scanBusyServer builds a server whose queue already holds unfinished work of
// the given task types for every source.
func scanBusyServer(t *testing.T, sources *fakeSources, unfinished map[jobs.TaskType]bool) *Server {
	t.Helper()
	jobsFake := &fakeJobs{unfinished: unfinished}
	return newTestServerWithJobs(t, sources, &fakeProfiles{}, &fakeLibrary{}, jobsFake, "")
}

func TestSourceDetailDisablesScanWhileAScanIsInFlight(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", Enabled: true}}
	server := scanBusyServer(t, sources, map[jobs.TaskType]bool{jobs.TaskIndexSource: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "Scanning…") {
		t.Error("an in-flight scan should render as Scanning…")
	}
	if strings.Contains(body, ">Scan now<") {
		t.Error("Scan now still offered while a scan is in flight")
	}
	if !strings.Contains(body, "Fix file names") {
		t.Error("renaming should stay available while only a scan is in flight")
	}
}

func TestSourceDetailDisablesRenameWhileARenameIsInFlight(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", Enabled: true}}
	server := scanBusyServer(t, sources, map[jobs.TaskType]bool{jobs.TaskRenameFiles: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "Renaming files…") {
		t.Error("an in-flight rename should render as Renaming files…")
	}
	if strings.Contains(body, ">Fix file names<") {
		t.Error("Fix file names still offered while a rename is in flight")
	}
}

func TestScanNowEnqueuesWhenIdleAndRedirects(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan"}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sources/7/scan", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/sources/7" {
		t.Errorf("Location = %q, want /sources/7", got)
	}
	if len(sources.scanned) != 1 || sources.scanned[0] != 7 {
		t.Errorf("scanned = %v, want [7]", sources.scanned)
	}
}

func TestScanNowSkipsWhenAScanIsAlreadyInFlight(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan"}}
	server := scanBusyServer(t, sources, map[jobs.TaskType]bool{jobs.TaskIndexSource: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sources/7/scan", nil))

	// Still a success — the scan the user wanted is happening — but no duplicate
	// is stacked behind it.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(sources.scanned) != 0 {
		t.Errorf("scanned = %v, want none while a scan is in flight", sources.scanned)
	}
}

func TestRenameSkipsWhenARenameIsAlreadyInFlight(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan"}}
	server := scanBusyServer(t, sources, map[jobs.TaskType]bool{jobs.TaskRenameFiles: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sources/7/rename", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(sources.renamed) != 0 {
		t.Errorf("renamed = %v, want none while a rename is in flight", sources.renamed)
	}
}

func TestSourceRenameEnqueuesAndRedirects(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", URL: "https://example.com/@chan"}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	req := httptest.NewRequest(http.MethodPost, "/sources/7/rename", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if location := rec.Header().Get("Location"); location != "/sources/7" {
		t.Errorf("Location = %q, want /sources/7", location)
	}
	if len(sources.renamed) != 1 || sources.renamed[0] != 7 {
		t.Errorf("renamed = %v, want [7]", sources.renamed)
	}
}

func TestSourceRetryFailedRedirectsWithCount(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", URL: "https://example.com/@chan"}}
	lib := &fakeLibrary{retryCount: 3}
	server := newTestServerFull(t, sources, &fakeProfiles{}, lib, "")

	req := httptest.NewRequest(http.MethodPost, "/sources/7/retry-failed", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/sources/7?retried=3" {
		t.Errorf("Location = %q, want /sources/7?retried=3", got)
	}
	if lib.retryCount != 3 {
		t.Errorf("RetryAllFailed returned %d, want 3", lib.retryCount)
	}
}

func TestSourceRetryFailedAlertShownWhenNonZero(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", URL: "https://example.com/@chan"}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7?retried=3", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "3 failed downloads have been requeued.") {
		t.Errorf("expected retry notice in body:\n%s", body)
	}
	if !strings.Contains(body, `class="alert alert-info"`) {
		t.Error("expected the alert-info class on the retry notice")
	}
}

func TestSourceRetryFailedAlertHiddenWhenAbsent(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", URL: "https://example.com/@chan"}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7", nil))
	body := rec.Body.String()

	if strings.Contains(body, "have been requeued") {
		t.Errorf("alert should not appear without ?retried, got:\n%s", body)
	}
}

func TestSourceRetryFailedAlertHiddenWhenZero(t *testing.T) {
	sources := &fakeSources{getResult: domain.Source{ID: 7, Name: "Chan", URL: "https://example.com/@chan"}}
	server := newTestServer(t, sources, &fakeProfiles{}, "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources/7?retried=0", nil))
	body := rec.Body.String()

	if strings.Contains(body, "have been requeued") {
		t.Errorf("alert should not appear when ?retried=0, got:\n%s", body)
	}
}
