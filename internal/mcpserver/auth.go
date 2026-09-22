package mcpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix is the Authorization scheme MCP clients send the token under.
const bearerPrefix = "Bearer "

// requireBearer gates the MCP endpoint behind a bearer token. MCP clients
// cannot complete the web UI's browser login, so the endpoint carries its own
// secret — configured by the operator, sent as "Authorization: Bearer <token>".
func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
		if !ok || !digestEqual(presented, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sub_scribe mcp"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// digestEqual compares two strings in constant time via their SHA-256 digests,
// so neither the timing nor the length of the configured secret leaks. It
// mirrors the web layer's helper; the two packages are peer adapters, so
// neither imports the other for six lines.
func digestEqual(got, want string) bool {
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}
