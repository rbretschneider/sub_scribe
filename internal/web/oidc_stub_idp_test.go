package web

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// stubIDP is a minimal test-only OIDC issuer: discovery, JWKS, and a token
// endpoint that redeems pre-arranged codes for RSA-signed ID tokens. It lets
// the full login flow run end to end with no outbound network connection.
type stubIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	mu    sync.Mutex
	seq   int
	codes map[string]stubIDTokenClaims
}

// stubIDTokenClaims shapes the ID token a code redeems for. Zero values fall
// back to a well-formed token for the stub's issuer and client.
type stubIDTokenClaims struct {
	Audience string
	Subject  string
	Nonce    string
	Email    string
}

func newStubIDP(t *testing.T) *stubIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	idp := &stubIDP{key: key, kid: "test-key-1", codes: make(map[string]stubIDTokenClaims)}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", idp.handleDiscovery)
	mux.HandleFunc("/token", idp.handleToken)
	mux.HandleFunc("/keys", idp.handleKeys)

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *stubIDP) issuer() string { return idp.server.URL }

// issueCode registers a code the token endpoint will redeem for an ID token
// with the given claims.
func (idp *stubIDP) issueCode(clientID string, claims stubIDTokenClaims) string {
	if claims.Audience == "" {
		claims.Audience = clientID
	}
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.seq++
	code := "stub-code-" + strconv.Itoa(idp.seq)
	idp.codes[code] = claims
	return code
}

func (idp *stubIDP) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeStubJSON(w, map[string]any{
		"issuer":                                idp.issuer(),
		"authorization_endpoint":                idp.issuer() + "/authorize",
		"token_endpoint":                        idp.issuer() + "/token",
		"jwks_uri":                              idp.issuer() + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (idp *stubIDP) handleKeys(w http.ResponseWriter, _ *http.Request) {
	writeStubJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: idp.key.Public(), KeyID: idp.kid, Algorithm: "RS256", Use: "sig",
	}}})
}

func (idp *stubIDP) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	idp.mu.Lock()
	claims, ok := idp.codes[r.Form.Get("code")]
	idp.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		writeStubJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}

	idToken, err := idp.signIDToken(claims)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeStubJSON(w, map[string]any{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

// signIDToken produces a compact RS256 JWT for the stub's issuer.
func (idp *stubIDP) signIDToken(c stubIDTokenClaims) (string, error) {
	now := time.Now()
	payload := map[string]any{
		"iss": idp.issuer(),
		"sub": c.Subject,
		"aud": c.Audience,
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
	}
	if c.Nonce != "" {
		payload["nonce"] = c.Nonce
	}
	if c.Email != "" {
		payload["email"] = c.Email
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	options := (&jose.SignerOptions{}).WithHeader("kid", idp.kid).WithType("JWT")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: idp.key}, options)
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(raw)
	if err != nil {
		return "", err
	}
	return signed.CompactSerialize()
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
