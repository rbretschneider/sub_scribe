// OIDC single sign-on: the Authorization Code + PKCE flow against one OpenID
// Connect issuer, on the trust-the-IdP model — any identity the provider
// authenticates and authorizes for this app gets in. Which identities those
// are is the provider's application/group binding, configured at the IdP, so
// this single-operator app needs no user table. On success the app mints its
// own session cookie (see session.go); IdP tokens are used once at the
// callback to read the identity and then dropped.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	goidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCOptions configures single sign-on. A zero IssuerURL leaves SSO off and
// every /auth route dormant. Mirrors config.OIDC so the web layer does not
// import the config package.
type OIDCOptions struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// ButtonLabel is the login page's sign-in button text.
	ButtonLabel string
}

const (
	// transactionCookieName carries the in-flight login's state, nonce, and
	// PKCE verifier between /start and /callback.
	transactionCookieName = "subscribe_oidc_tx"
	// transactionCookiePath scopes the cookie off every other request.
	transactionCookiePath = "/auth/oidc"
	// transactionMaxAge bounds one login round trip through the IdP.
	transactionMaxAge = 5 * time.Minute

	// transactionKeyLabel derives the transaction MAC key from the session
	// secret. The distinct label guarantees a subscribe_session value can never
	// verify as a transaction and vice versa, independent of encoding.
	transactionKeyLabel = "subscribe-oidc-tx-v1"

	// oidcTimeout bounds discovery and the token exchange; anything slower is
	// an IdP problem the user should hear about, not wait through.
	oidcTimeout = 10 * time.Second

	// ssoFailedQuery is the login page query string shown after a failed
	// attempt. Every callback failure lands here — the login page always
	// renders, never a dead end.
	ssoFailedQuery = "?error=sso_failed"
)

// Transaction-verification sentinels, so tests assert which check rejected a
// value. The browser sees only a redirect back to the login page either way.
var (
	errTransactionMAC       = errors.New("web: oidc transaction MAC invalid")
	errTransactionMalformed = errors.New("web: oidc transaction malformed")
	errTransactionExpired   = errors.New("web: oidc transaction expired")
)

// oidcTransaction carries the state, nonce, and PKCE verifier for one login
// round trip. It lives only in the MAC'd transaction cookie, never the
// database.
type oidcTransaction struct {
	State        string `json:"s"`
	Nonce        string `json:"n"`
	CodeVerifier string `json:"v"`
	IssuedAt     int64  `json:"t"` // unix seconds
}

// deriveTransactionKey computes HMAC(secret, label) so the transaction MAC key
// is distinct from the raw session-signing secret.
func deriveTransactionKey(sessionSecret []byte) []byte {
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(transactionKeyLabel))
	return mac.Sum(nil)
}

// encode serializes and MACs the transaction with the same shape the session
// codec uses: base64url(JSON) + "." + base64url(HMAC-SHA256).
func (t oidcTransaction) encode(txKey []byte) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return encoded + "." + macOf(encoded, txKey), nil
}

// decodeTransaction verifies and decodes a transaction cookie value. The MAC
// is checked before any decoding, so a value that is not a transaction at all
// (a subscribe_session cookie, say) dies on cryptographic grounds, never on a
// downstream parse accident.
func decodeTransaction(raw string, txKey []byte, now time.Time) (oidcTransaction, error) {
	var zero oidcTransaction

	dot := strings.LastIndex(raw, ".")
	if raw == "" || dot < 0 {
		return zero, errTransactionMalformed
	}
	encoded, signature := raw[:dot], raw[dot+1:]

	given, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(given, rawMacOf(encoded, txKey)) {
		return zero, errTransactionMAC
	}

	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return zero, errTransactionMalformed
	}
	var tx oidcTransaction
	if err := json.Unmarshal(decoded, &tx); err != nil {
		return zero, errTransactionMalformed
	}
	if tx.State == "" || tx.Nonce == "" || tx.CodeVerifier == "" || tx.IssuedAt <= 0 {
		return zero, errTransactionMalformed
	}
	if now.Sub(time.Unix(tx.IssuedAt, 0)) > transactionMaxAge {
		return zero, errTransactionExpired
	}
	return tx, nil
}

// oidcService owns the protocol against the configured issuer. Discovery is
// lazy and cached only on success, so a down IdP never breaks startup and a
// recovered one works on the next attempt without a restart.
type oidcService struct {
	options OIDCOptions
	// httpClient overrides the discovery/exchange client; nil uses a default
	// with oidcTimeout. Tests point it at a stub IdP.
	httpClient *http.Client

	mu       sync.Mutex
	provider *goidc.Provider
}

// identity is what the app keeps from a verified ID token: enough to log who
// signed in, and nothing that outlives the callback.
type identity struct {
	Subject string
	Email   string
}

// newOIDCService wires the protocol handler for the given options.
func newOIDCService(options OIDCOptions) *oidcService {
	return &oidcService{options: options}
}

// discover returns the issuer's cached discovery document, fetching it on
// first use. Failures are never cached.
func (o *oidcService) discover(ctx context.Context) (*goidc.Provider, error) {
	o.mu.Lock()
	cached := o.provider
	o.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	provider, err := goidc.NewProvider(goidc.ClientContext(ctx, o.client()), o.options.IssuerURL)
	if err != nil {
		return nil, err
	}

	o.mu.Lock()
	o.provider = provider
	o.mu.Unlock()
	return provider, nil
}

// client returns the HTTP client used for discovery and the token exchange.
func (o *oidcService) client() *http.Client {
	if o.httpClient != nil {
		return o.httpClient
	}
	return &http.Client{Timeout: oidcTimeout}
}

// oauth2Config assembles the flow configuration for one request's redirect URI.
func (o *oidcService) oauth2Config(provider *goidc.Provider, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.options.ClientID,
		ClientSecret: o.options.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       []string{goidc.ScopeOpenID, "email", "profile"},
	}
}

// beginLogin performs discovery and returns the IdP authorization URL plus the
// transaction that must round-trip through the cookie.
func (o *oidcService) beginLogin(ctx context.Context, redirectURI string, now time.Time) (string, oidcTransaction, error) {
	ctx, cancel := context.WithTimeout(ctx, oidcTimeout)
	defer cancel()

	provider, err := o.discover(ctx)
	if err != nil {
		return "", oidcTransaction{}, err
	}

	state, err := randomValue()
	if err != nil {
		return "", oidcTransaction{}, err
	}
	nonce, err := randomValue()
	if err != nil {
		return "", oidcTransaction{}, err
	}
	verifier := oauth2.GenerateVerifier()

	authURL := o.oauth2Config(provider, redirectURI).
		AuthCodeURL(state, goidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))

	return authURL, oidcTransaction{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: verifier,
		IssuedAt:     now.Unix(),
	}, nil
}

// completeLogin exchanges the authorization code and verifies the ID token —
// issuer, audience, signature via the library, then the transaction's nonce.
func (o *oidcService) completeLogin(ctx context.Context, redirectURI, code string, tx oidcTransaction) (identity, error) {
	ctx, cancel := context.WithTimeout(ctx, oidcTimeout)
	defer cancel()

	provider, err := o.discover(ctx)
	if err != nil {
		return identity{}, err
	}
	if code == "" {
		return identity{}, errors.New("web: oidc callback carried no code")
	}

	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, o.client())
	token, err := o.oauth2Config(provider, redirectURI).
		Exchange(exchangeCtx, code, oauth2.VerifierOption(tx.CodeVerifier))
	if err != nil {
		return identity{}, err
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return identity{}, errors.New("web: token response carried no id_token")
	}

	idToken, err := provider.Verifier(&goidc.Config{ClientID: o.options.ClientID}).
		Verify(ctx, rawIDToken)
	if err != nil {
		return identity{}, err
	}
	if idToken.Nonce != tx.Nonce {
		return identity{}, errors.New("web: id_token nonce mismatch")
	}

	var extra struct {
		Email string `json:"email"`
	}
	_ = idToken.Claims(&extra) // best-effort: a missing email claim is not fatal

	return identity{Subject: idToken.Subject, Email: extra.Email}, nil
}

// randomValue returns a base64url value from 32 bytes of crypto/rand — 256
// bits of entropy for the state and nonce.
func randomValue() (string, error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// oidcConfigured reports whether SSO is on for this server.
func (s *Server) oidcConfigured() bool {
	return s.oidc != nil
}

// redirectURI derives the callback address from the request itself, so the
// same binary works on any host name without extra configuration. The exact
// same URI must be registered at the IdP.
func redirectURI(r *http.Request) string {
	scheme := "http"
	if requestIsTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + oidcCallbackPath
}

// handleLogin renders the login page. A browser that is already authorized has
// nothing to do here and goes home instead.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		http.NotFound(w, r)
		return
	}
	if s.authorized(r) {
		redirect(w, r, "/")
		return
	}
	view := loginView{
		Title:          "Sign in",
		ButtonLabel:    s.oidc.options.ButtonLabel,
		BasicAvailable: s.basicConfigured(),
	}
	if r.URL.Query().Get("error") != "" {
		view.Error = "Signing in didn't work. Try again, and check the app's logs if it keeps failing."
	}
	s.render(w, "login", http.StatusOK, view)
}

// loginView is the login page's template data.
type loginView struct {
	Title       string
	ButtonLabel string
	// Error is a human-readable explanation when the previous attempt failed.
	Error string
	// BasicAvailable notes that scripts and feed readers can still use HTTP
	// basic auth alongside SSO.
	BasicAvailable bool
}

// handleOIDCStart begins the login round trip: PKCE, state, and nonce go into
// the MAC'd transaction cookie, and the browser goes to the IdP.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		http.NotFound(w, r)
		return
	}

	authURL, tx, err := s.oidc.beginLogin(r.Context(), redirectURI(r), s.deps.Clock.Now())
	if err != nil {
		slog.Warn("SSO start failed", "error", err)
		redirect(w, r, loginPath+ssoFailedQuery)
		return
	}

	encoded, err := tx.encode(deriveTransactionKey(s.deps.SessionSecret))
	if err != nil {
		slog.Warn("SSO start failed", "error", err)
		redirect(w, r, loginPath+ssoFailedQuery)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     transactionCookieName,
		Value:    encoded,
		Path:     transactionCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   int(transactionMaxAge / time.Second),
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleOIDCCallback finishes the round trip: verify the transaction, exchange
// the code, verify the ID token, and mint the session. Every failure clears
// the transaction cookie and lands on the login page with a readable error —
// never a dead end.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		http.NotFound(w, r)
		return
	}

	// Cleared before any branch so every outcome — success included — retires
	// the single-use transaction.
	s.clearTransactionCookie(w, r)

	fail := func(reason string, err error) {
		slog.Warn("SSO login failed", "reason", reason, "error", err)
		redirect(w, r, loginPath+ssoFailedQuery)
	}

	var rawTx string
	if cookie, err := r.Cookie(transactionCookieName); err == nil {
		rawTx = cookie.Value
	}
	tx, err := decodeTransaction(rawTx, deriveTransactionKey(s.deps.SessionSecret), s.deps.Clock.Now())
	if err != nil {
		fail("transaction rejected", err)
		return
	}

	if idpErr := r.URL.Query().Get("error"); idpErr != "" {
		fail("IdP returned an error", errors.New(idpErr))
		return
	}
	if state := r.URL.Query().Get("state"); !constantTimeEqual(state, tx.State) {
		fail("state mismatch", nil)
		return
	}

	who, err := s.oidc.completeLogin(r.Context(), redirectURI(r), r.URL.Query().Get("code"), tx)
	if err != nil {
		fail("token exchange or verification failed", err)
		return
	}

	session, err := mintSession(s.deps.SessionSecret, s.deps.Clock.Now())
	if err != nil {
		fail("minting session failed", err)
		return
	}
	s.setSessionCookie(w, r, session)

	slog.Info("SSO login", "email", who.Email, "sub", who.Subject)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLogout clears the session and returns the browser to the login page.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		http.NotFound(w, r)
		return
	}
	s.clearSessionCookie(w, r)
	redirect(w, r, loginPath)
}

// clearTransactionCookie retires the single-use login transaction with the
// same attributes it was set with.
func (s *Server) clearTransactionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     transactionCookieName,
		Value:    "",
		Path:     transactionCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   -1,
	})
}

// constantTimeEqual compares two strings without leaking where they diverge.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
