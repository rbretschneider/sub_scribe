package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"sub_scribe/internal/domain"
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
