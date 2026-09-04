package web

import (
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

// testSessionSecret is a fixed signing secret so cookies minted by one helper
// verify in another.
var testSessionSecret = []byte("0123456789abcdef0123456789abcdef")

// testOIDC is a syntactically valid SSO configuration for servers that never
// reach the IdP; flow tests overwrite the issuer with a stub's URL.
var testOIDC = OIDCOptions{
	IssuerURL:    "https://idp.example.com/application/o/subscribe/",
	ClientID:     "subscribe-client",
	ClientSecret: "subscribe-secret",
	ButtonLabel:  "Sign in with SSO",
}

// oidcServerOptions selects which auth methods a test server has configured.
type oidcServerOptions struct {
	oidc     bool
	username string
	password string
	sources  *fakeSources
}

// newAuthTestServer builds a Server with any combination of OIDC and basic
// auth, on the same fakes the rest of the suite uses.
func newAuthTestServer(t *testing.T, opts oidcServerOptions) *Server {
	t.Helper()
	deps := ServerDeps{
		Sources:     &fakeSources{},
		Profiles:    &fakeProfiles{},
		Library:     &fakeLibrary{},
		Jobs:        &fakeJobs{},
		Queue:       &fakeJobs{},
		Logs:        applog.NewBuffer(0),
		CookiesPath: filepath.Join(t.TempDir(), "cookies.txt"),
		Clock:       fixedClock{now: testNow},
		EventsPath:  "/events",
		Username:    opts.username,
		Password:    opts.password,
	}
	if opts.sources != nil {
		deps.Sources = opts.sources
	}
	if opts.oidc {
		deps.OIDC = testOIDC
		deps.SessionSecret = testSessionSecret
	}
	server, err := NewServer(deps)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

// mintTestSession returns a valid session cookie value as of the fixed clock.
func mintTestSession(t *testing.T) string {
	t.Helper()
	session, err := mintSession(testSessionSecret, testNow)
	if err != nil {
		t.Fatalf("mintSession() error = %v", err)
	}
	return session
}

// get builds a GET request, optionally shaped as a top-level browser
// navigation (Sec-Fetch-Dest: document) or a fetch/EventSource call.
func getRequest(path string, document bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if document {
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
	} else {
		req.Header.Set("Sec-Fetch-Dest", "empty")
	}
	return req
}

func withSession(t *testing.T, req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mintTestSession(t)})
	return req
}

func withBasic(req *http.Request, user, pass string) *http.Request {
	req.SetBasicAuth(user, pass)
	return req
}

func TestGateMatrix(t *testing.T) {
	tests := []struct {
		name       string
		oidc       bool
		basic      bool
		session    bool
		basicCreds bool
		document   bool
		wantStatus int
		// wantLocation, when set, asserts where a redirect points.
		wantLocation string
		// wantChallenge asserts the presence/absence of WWW-Authenticate on 401s.
		wantChallenge bool
	}{
		{name: "nothing configured stays open", wantStatus: http.StatusOK, document: true},
		{name: "nothing configured stays open for fetches", wantStatus: http.StatusOK},

		{name: "basic only still challenges documents", basic: true, document: true,
			wantStatus: http.StatusUnauthorized, wantChallenge: true},
		{name: "basic only still challenges fetches", basic: true,
			wantStatus: http.StatusUnauthorized, wantChallenge: true},
		{name: "basic only accepts credentials", basic: true, basicCreds: true, document: true,
			wantStatus: http.StatusOK},

		{name: "oidc redirects an anonymous document to the login page", oidc: true, document: true,
			wantStatus: http.StatusFound, wantLocation: "/auth/login"},
		{name: "oidc gives an anonymous fetch a plain 401", oidc: true,
			wantStatus: http.StatusUnauthorized, wantChallenge: false},
		{name: "oidc accepts a session for documents", oidc: true, session: true, document: true,
			wantStatus: http.StatusOK},
		{name: "oidc accepts a session for fetches", oidc: true, session: true,
			wantStatus: http.StatusOK},
		{name: "oidc without basic rejects basic credentials", oidc: true, basicCreds: true,
			wantStatus: http.StatusUnauthorized},

		{name: "oidc plus basic redirects an anonymous document", oidc: true, basic: true, document: true,
			wantStatus: http.StatusFound, wantLocation: "/auth/login"},
		{name: "oidc plus basic gives an anonymous fetch a 401 without a challenge",
			oidc: true, basic: true, wantStatus: http.StatusUnauthorized, wantChallenge: false},
		{name: "oidc plus basic accepts a session", oidc: true, basic: true, session: true, document: true,
			wantStatus: http.StatusOK},
		{name: "oidc plus basic accepts basic credentials for scripts", oidc: true, basic: true, basicCreds: true,
			wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := oidcServerOptions{oidc: tc.oidc}
			if tc.basic {
				opts.username, opts.password = "ryan", "hunter2"
			}
			server := newAuthTestServer(t, opts)

			req := getRequest("/", tc.document)
			if tc.session {
				req = withSession(t, req)
			}
			if tc.basicCreds {
				req = withBasic(req, "ryan", "hunter2")
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantLocation != "" && rec.Header().Get("Location") != tc.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tc.wantLocation)
			}
			if rec.Code == http.StatusUnauthorized {
				hasChallenge := rec.Header().Get("WWW-Authenticate") != ""
				if hasChallenge != tc.wantChallenge {
					t.Errorf("WWW-Authenticate present = %v, want %v", hasChallenge, tc.wantChallenge)
				}
			}
		})
	}
}

func TestPublicPathsAreExemptOnlyWithOIDC(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	// GET public paths render (or redirect into the flow) without any auth.
	for path, wantStatus := range map[string]int{
		"/auth/login":     http.StatusOK,
		"/static/app.css": http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, getRequest(path, true))
		if rec.Code != wantStatus {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, wantStatus)
		}
	}

	// Logout is public so an expired session can still be cleared.
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST /auth/logout = %d, want 303", rec.Code)
	}

	// Everything else stays gated — including the event stream.
	for _, path := range []string{"/events", "/sources", "/library", "/jobs"} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, getRequest(path, false))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}

func TestLoginPageIsNotFoundWhenOIDCIsOff(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/login", true))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when SSO is dormant", rec.Code)
	}
}

func TestLoginPageShowsButtonAndError(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true, username: "ryan", password: "hunter2"})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/login?error=sso_failed", true))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in with SSO") || !strings.Contains(body, "/auth/oidc/start") {
		t.Error("login page missing the SSO button")
	}
	if !strings.Contains(body, "didn&#39;t work") {
		t.Error("login page missing the failure explanation for ?error=")
	}
	if !strings.Contains(body, "basic auth") {
		t.Error("login page missing the basic-auth note when credentials are configured")
	}
	if strings.Contains(body, "sidebar") || strings.Contains(body, `class="nav"`) {
		t.Error("login page leaked the application chrome")
	}
}

func TestLoggedInBrowserIsSentHomeFromTheLoginPage(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, withSession(t, getRequest("/auth/login", true)))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 home", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

func TestSessionCookieForgeryIsRejected(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	// A payload signed with the wrong key must not pass.
	forged, err := mintSession([]byte("attacker-controlled-secret-32byte"), testNow)
	if err != nil {
		t.Fatalf("mintSession() error = %v", err)
	}
	req := getRequest("/", false)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: forged})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged session status = %d, want 401", rec.Code)
	}

	if err := verifySession(forged, testSessionSecret, testNow); err != errSessionMAC {
		t.Errorf("verifySession(forged) = %v, want errSessionMAC", err)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	expired, err := mintSession(testSessionSecret, testNow.Add(-sessionTTL-time.Minute))
	if err != nil {
		t.Fatalf("mintSession() error = %v", err)
	}
	if err := verifySession(expired, testSessionSecret, testNow); err != errSessionExpired {
		t.Fatalf("verifySession(expired) = %v, want errSessionExpired", err)
	}

	server := newAuthTestServer(t, oidcServerOptions{oidc: true})
	req := getRequest("/", true)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: expired})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("expired session document status = %d, want 302 to login", rec.Code)
	}
}

func TestSessionValuePresentedAsTransactionDiesAtTheMAC(t *testing.T) {
	// The keys are domain-separated: a subscribe_session value must be
	// rejected as a transaction on cryptographic grounds, before any parsing.
	session := mintTestSession(t)
	txKey := deriveTransactionKey(testSessionSecret)

	if _, err := decodeTransaction(session, txKey, testNow); err != errTransactionMAC {
		t.Fatalf("decodeTransaction(session value) = %v, want errTransactionMAC", err)
	}
}

func TestTransactionRoundTripAndExpiry(t *testing.T) {
	txKey := deriveTransactionKey(testSessionSecret)
	tx := oidcTransaction{State: "s", Nonce: "n", CodeVerifier: "v", IssuedAt: testNow.Unix()}
	encoded, err := tx.encode(txKey)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := decodeTransaction(encoded, txKey, testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("decodeTransaction() error = %v", err)
	}
	if decoded != tx {
		t.Errorf("decoded = %+v, want %+v", decoded, tx)
	}

	if _, err := decodeTransaction(encoded, txKey, testNow.Add(transactionMaxAge+time.Minute)); err != errTransactionExpired {
		t.Errorf("stale transaction error = %v, want errTransactionExpired", err)
	}
}

func TestCallbackWithoutATransactionLandsOnTheLoginPageWithAnError(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/oidc/callback?code=x&state=y", true))

	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/auth/login?error=sso_failed" {
		t.Errorf("Location = %q, want the login page with an error", got)
	}
}

func TestLogoutClearsTheSessionCookie(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mintTestSession(t)})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 && cookie.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
}

func TestSignOutButtonAppearsOnlyWithASession(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, withSession(t, getRequest("/", true)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/auth/logout") {
		t.Error("a signed-in page should offer the sign-out button")
	}

	// Basic auth without OIDC has nothing to sign out of.
	basicOnly := newAuthTestServer(t, oidcServerOptions{username: "ryan", password: "hunter2"})
	rec = httptest.NewRecorder()
	server2req := withBasic(getRequest("/", true), "ryan", "hunter2")
	basicOnly.ServeHTTP(rec, server2req)
	if rec.Code != http.StatusOK {
		t.Fatalf("basic status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/auth/logout") {
		t.Error("a basic-auth page should not offer the sign-out button")
	}
}

// feedSources returns a fakeSources whose GetSource result carries a token.
func feedSources(token string) *fakeSources {
	return &fakeSources{getResult: domain.Source{ID: 7, Name: "Cool Channel", FeedToken: token}}
}

func TestFeedTokenAuthorizesTheFeedRegardlessOfAuthMode(t *testing.T) {
	token := "aaaabbbbccccddddeeeeffff00001111"

	for _, opts := range []oidcServerOptions{
		{oidc: true, sources: feedSources(token)},
		{username: "ryan", password: "hunter2", sources: feedSources(token)},
		{oidc: true, username: "ryan", password: "hunter2", sources: feedSources(token)},
	} {
		server := newAuthTestServer(t, opts)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, getRequest("/feeds/7?t="+token, false))
		// 404 is "authorized but no feed file yet" — the gate let it through.
		if rec.Code != http.StatusNotFound {
			t.Errorf("opts %+v: status = %d, want 404 past the gate", opts, rec.Code)
		}
	}
}

func TestFeedWithWrongOrMissingTokenStaysGated(t *testing.T) {
	token := "aaaabbbbccccddddeeeeffff00001111"
	server := newAuthTestServer(t, oidcServerOptions{oidc: true, sources: feedSources(token)})

	for name, path := range map[string]string{
		"wrong token":   "/feeds/7?t=wrong",
		"missing token": "/feeds/7",
		"empty token":   "/feeds/7?t=",
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, getRequest(path, false))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
}

func TestFeedTokenNeverMatchesASourceWithoutOne(t *testing.T) {
	// A pre-backfill row (empty stored token) must not be unlocked by ?t=.
	server := newAuthTestServer(t, oidcServerOptions{oidc: true, sources: feedSources("")})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/feeds/7?t=", false))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an empty stored token", rec.Code)
	}
}

func TestUntokenizedFeedKeepsWorkingWithNoAuthConfigured(t *testing.T) {
	server := newAuthTestServer(t, oidcServerOptions{sources: feedSources("")})

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/feeds/7", false))
	// 404 = past the gate, feed file simply not written yet.
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 past the gate", rec.Code)
	}
}

func TestStartRedirectsToTheLoginPageWhenTheIdPIsDown(t *testing.T) {
	// The issuer in testOIDC does not resolve, so discovery fails; the user
	// must land back on the login page with an error, never a dead end.
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})
	server.oidc.httpClient = &http.Client{Timeout: 200 * time.Millisecond}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/oidc/start", true))

	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || location.Path != "/auth/login" {
		t.Errorf("Location = %q, want the login page", rec.Header().Get("Location"))
	}
}
