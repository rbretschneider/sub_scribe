package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"sub_scribe/internal/applog"
)

// authServer builds a Server locked with the given credentials.
func authServer(t *testing.T, username, password string) *Server {
	t.Helper()
	server, err := NewServer(ServerDeps{
		Sources:     &fakeSources{},
		Profiles:    &fakeProfiles{},
		Library:     &fakeLibrary{},
		Jobs:        &fakeJobs{},
		Queue:       &fakeJobs{},
		Logs:        applog.NewBuffer(0),
		CookiesPath: filepath.Join(t.TempDir(), "cookies.txt"),
		Clock:       fixedClock{now: testNow},
		EventsPath:  "/events",
		Username:    username,
		Password:    password,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func TestAuthRejectsRequestsWithoutCredentials(t *testing.T) {
	server := authServer(t, "ryan", "hunter2")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 must carry WWW-Authenticate so the browser prompts")
	}
}

func TestAuthAcceptsTheRightCredentials(t *testing.T) {
	server := authServer(t, "ryan", "hunter2")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("ryan", "hunter2")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRejectsTheWrongPassword(t *testing.T) {
	server := authServer(t, "ryan", "hunter2")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("ryan", "wrong")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNoCredentialsConfiguredLeavesTheServerOpen(t *testing.T) {
	server := authServer(t, "", "")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no auth is configured", rec.Code)
	}
}

func TestProtectGuardsAHandlerMountedBesideTheServer(t *testing.T) {
	server := authServer(t, "ryan", "hunter2")
	guarded := server.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.SetBasicAuth("ryan", "hunter2")
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("authenticated status = %d, want 204", rec.Code)
	}
}
