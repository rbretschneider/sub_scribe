package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newFlowTestServer builds a Server whose OIDC issuer is the stub IdP.
func newFlowTestServer(t *testing.T, idp *stubIDP) *Server {
	t.Helper()
	server := newAuthTestServer(t, oidcServerOptions{oidc: true})
	server.oidc.options.IssuerURL = idp.issuer()
	return server
}

// startLogin drives GET /auth/oidc/start and returns the IdP redirect's query
// values plus the transaction cookie the browser would hold.
func startLogin(t *testing.T, server *Server) (url.Values, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/oidc/start", true))
	if rec.Code != http.StatusFound {
		t.Fatalf("start status = %d, want 302 to the IdP; body: %s", rec.Code, rec.Body.String())
	}

	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if !strings.HasSuffix(location.Path, "/authorize") {
		t.Fatalf("start redirected to %q, want the IdP authorize endpoint", location)
	}
	query := location.Query()
	if query.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (PKCE)", query.Get("code_challenge_method"))
	}
	if query.Get("state") == "" || query.Get("nonce") == "" {
		t.Error("authorize URL missing state or nonce")
	}

	var tx *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == transactionCookieName {
			tx = cookie
		}
	}
	if tx == nil {
		t.Fatal("start did not set the transaction cookie")
	}
	return query, tx
}

// finishLogin drives the callback with the given query and cookie, returning
// the recorder for assertions.
func finishLogin(t *testing.T, server *Server, state, code string, tx *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := getRequest("/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), true)
	req.AddCookie(tx)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func TestFullSSOLoginFlowMintsAWorkingSession(t *testing.T) {
	idp := newStubIDP(t)
	server := newFlowTestServer(t, idp)

	query, tx := startLogin(t, server)
	code := idp.issueCode(testOIDC.ClientID, stubIDTokenClaims{
		Subject: "ryan-sub", Nonce: query.Get("nonce"), Email: "ryan@example.com",
	})

	rec := finishLogin(t, server, query.Get("state"), code, tx)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302 home; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("callback Location = %q, want /", got)
	}

	var session *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			session = cookie
		case transactionCookieName:
			if cookie.MaxAge >= 0 {
				t.Error("callback did not retire the transaction cookie")
			}
		}
	}
	if session == nil {
		t.Fatal("callback did not set the session cookie")
	}
	if !session.HttpOnly || session.Path != "/" {
		t.Errorf("session cookie attributes = HttpOnly:%v Path:%q, want HttpOnly on Path /",
			session.HttpOnly, session.Path)
	}

	// The minted session actually opens the app.
	req := getRequest("/", true)
	req.AddCookie(session)
	loggedIn := httptest.NewRecorder()
	server.ServeHTTP(loggedIn, req)
	if loggedIn.Code != http.StatusOK {
		t.Fatalf("request with minted session = %d, want 200", loggedIn.Code)
	}
}

func TestCallbackRejectsAStateMismatch(t *testing.T) {
	idp := newStubIDP(t)
	server := newFlowTestServer(t, idp)

	query, tx := startLogin(t, server)
	code := idp.issueCode(testOIDC.ClientID, stubIDTokenClaims{
		Subject: "ryan-sub", Nonce: query.Get("nonce"),
	})

	rec := finishLogin(t, server, "attacker-supplied-state", code, tx)
	if got := rec.Header().Get("Location"); got != "/auth/login?error=sso_failed" {
		t.Errorf("Location = %q, want the login page with an error", got)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			t.Error("a state mismatch must not mint a session")
		}
	}
}

func TestCallbackRejectsANonceMismatch(t *testing.T) {
	idp := newStubIDP(t)
	server := newFlowTestServer(t, idp)

	query, tx := startLogin(t, server)
	// The IdP signs a token bound to a different login attempt's nonce.
	code := idp.issueCode(testOIDC.ClientID, stubIDTokenClaims{
		Subject: "ryan-sub", Nonce: "some-other-nonce",
	})

	rec := finishLogin(t, server, query.Get("state"), code, tx)
	if got := rec.Header().Get("Location"); got != "/auth/login?error=sso_failed" {
		t.Errorf("Location = %q, want the login page with an error", got)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			t.Error("a nonce mismatch must not mint a session")
		}
	}
}

func TestCallbackRecoversAfterTheIdPComesBack(t *testing.T) {
	// Discovery failure is not cached: a login attempted while the IdP was
	// down succeeds later without restarting the app.
	idp := newStubIDP(t)
	server := newFlowTestServer(t, idp)

	down := newStubIDP(t)
	down.server.Close()
	server.oidc.options.IssuerURL = down.issuer()

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, getRequest("/auth/oidc/start", true))
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location.Path != "/auth/login" {
		t.Fatalf("start with a down IdP went to %q, want the login page", location)
	}

	// The IdP "recovers" (points back at the live stub); the next attempt works.
	server.oidc.options.IssuerURL = idp.issuer()
	query, tx := startLogin(t, server)
	code := idp.issueCode(testOIDC.ClientID, stubIDTokenClaims{Subject: "ryan-sub", Nonce: query.Get("nonce")})
	callback := finishLogin(t, server, query.Get("state"), code, tx)
	if got := callback.Header().Get("Location"); got != "/" {
		t.Errorf("recovered login Location = %q, want /", got)
	}
}
