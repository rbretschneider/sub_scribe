// Session cookies: after a successful SSO login the browser holds a signed,
// stateless subscribe_session cookie instead of re-authenticating with the IdP
// on every request. The cookie is a random id plus an expiry, HMAC-signed with
// the persistent secret from the settings table — nothing is stored server
// side, so logout is simply clearing the cookie and restarts cost nothing.
package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// sessionCookieName is the browser session minted by a successful SSO login.
	sessionCookieName = "subscribe_session"
	// sessionTTL is how long a login lasts before the IdP must be consulted
	// again. Sessions are stateless, so there is no sliding renewal — a week is
	// long enough to not nag and short enough to honor an IdP-side revocation.
	sessionTTL = 7 * 24 * time.Hour
	// sessionIDBytes sizes the random session id: 32 bytes, 256 bits of entropy.
	sessionIDBytes = 32
)

// Session-verification sentinels, so tests assert which check rejected a
// cookie. The browser never sees the difference — every failure is simply an
// unauthenticated request.
var (
	errSessionMAC       = errors.New("web: session MAC invalid")
	errSessionMalformed = errors.New("web: session malformed")
	errSessionExpired   = errors.New("web: session expired")
)

// sessionPayload is the signed content of the session cookie.
type sessionPayload struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"exp"` // unix seconds
}

// mintSession returns a signed session cookie value valid for sessionTTL.
func mintSession(secret []byte, now time.Time) (string, error) {
	id := make([]byte, sessionIDBytes)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("web: generate session id: %w", err)
	}
	payload := sessionPayload{
		ID:        base64.RawURLEncoding.EncodeToString(id),
		ExpiresAt: now.Add(sessionTTL).Unix(),
	}
	return signPayload(payload, secret)
}

// signPayload serializes and MACs a session:
// base64url(JSON(payload)) + "." + base64url(HMAC-SHA256(secret, base64url(JSON(payload)))).
func signPayload(payload sessionPayload, secret []byte) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("web: encode session: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return encoded + "." + macOf(encoded, secret), nil
}

// verifySession checks a session cookie value and returns nil when it grants
// access as of now. The MAC is verified before anything is decoded, so a value
// that is not a session at all is rejected on cryptographic grounds, never on
// a downstream parse accident.
func verifySession(raw string, secret []byte, now time.Time) error {
	dot := strings.LastIndex(raw, ".")
	if raw == "" || dot < 0 {
		return errSessionMalformed
	}
	encoded, signature := raw[:dot], raw[dot+1:]

	given, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(given, rawMacOf(encoded, secret)) {
		return errSessionMAC
	}

	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return errSessionMalformed
	}
	var payload sessionPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return errSessionMalformed
	}
	if payload.ID == "" || payload.ExpiresAt <= 0 {
		return errSessionMalformed
	}
	if now.Unix() >= payload.ExpiresAt {
		return errSessionExpired
	}
	return nil
}

// macOf returns the base64url HMAC-SHA256 of message under key.
func macOf(message string, key []byte) string {
	return base64.RawURLEncoding.EncodeToString(rawMacOf(message, key))
}

// rawMacOf returns the raw HMAC-SHA256 of message under key.
func rawMacOf(message string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// hasValidSession reports whether the request carries a session cookie that
// verifies against the signing secret and has not expired.
func (s *Server) hasValidSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySession(cookie.Value, s.deps.SessionSecret, s.deps.Clock.Now()) == nil
}

// setSessionCookie sends a freshly minted session to the browser.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   int(sessionTTL / time.Second),
	})
}

// clearSessionCookie logs the browser out by expiring the cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   -1,
	})
}

// requestIsTLS reports whether the request reached the user over HTTPS —
// directly, or via a reverse proxy that terminated TLS and said so in
// X-Forwarded-Proto (the house nginx does). It decides cookie Secure flags
// and the scheme of the OIDC redirect URI.
func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
