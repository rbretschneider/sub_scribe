// Package web is sub_scribe's server-rendered UI: an http.Handler that lists
// sources, drives the add-source flow, and makes YouTube-cookie setup trivial via
// a drag-and-drop token upload. Templates and static assets are embedded so the
// binary is fully self-contained, and the layer depends only on the library
// service interfaces and the cookies assessor, keeping it fakeable in tests.
package web

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// LogReader supplies application log records for the in-app log viewer and for
// the per-job log panels. Implemented by the applog buffer.
type LogReader interface {
	// Recent returns the newest records first, for the global viewer.
	Recent(limit int) []applog.Record
	// ForTask returns the records a single job produced, oldest first.
	ForTask(taskID int64, limit int) []applog.Record
}

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// layoutTemplate is the base template every page is rendered through; page files
// fill its "content" and "title" blocks.
const layoutTemplate = "layout.html"

// authLayoutTemplate is the minimal chrome-free layout for pages shown to a
// browser that has not signed in yet — no sidebar, no nav, no token badge.
const authLayoutTemplate = "auth_layout.html"

// pageNames are the content templates, each composed with the layout at startup
// so a "content" block from one page never collides with another's.
var pageNames = []string{
	"dashboard", "library", "media_detail", "source_form", "source_detail",
	"token", "profiles", "profile_form", "sources", "logs", "jobs", "job_detail",
	"download_form", "login",
}

// pageLayouts names the layout a page renders through when it is not the
// default application layout.
var pageLayouts = map[string]string{
	"login": authLayoutTemplate,
}

// layoutFor resolves the layout template a page is composed with.
func layoutFor(page string) string {
	if layout, ok := pageLayouts[page]; ok {
		return layout
	}
	return layoutTemplate
}

// ServerDeps are the collaborators the web layer needs. All are interfaces or
// plain values so the server is constructed and tested without a real database,
// filesystem cookies, or wall clock.
type ServerDeps struct {
	Sources  library.SourceService
	Profiles library.ProfileService
	Library  library.LibraryReader
	// Jobs and Queue back the queue screens: Jobs reads what is running, queued,
	// and failed; Queue is only used by the retry action.
	Jobs        library.JobReader
	Queue       library.QueueMaintain
	Media       library.MediaService
	Logs        LogReader
	CookiesPath string
	// FeedDir is where the feed writer puts each source's podcast RSS file,
	// served read-only at /feeds/{id} so podcast apps can subscribe.
	FeedDir    string
	Clock      jobs.Clock
	EventsPath string
	// Username and Password, when both set, require HTTP basic auth on every
	// request. Both empty leaves the server open (a trusted-LAN deployment).
	Username string
	Password string
	// OIDC, when its IssuerURL is set, adds browser single sign-on with a
	// session cookie and a login page. It may be combined with basic auth,
	// which then keeps serving scripts and feed readers.
	OIDC OIDCOptions
	// SessionSecret signs the session and login-transaction cookies. Required
	// when OIDC is configured; persisted in the settings table by the caller.
	SessionSecret []byte
}

// Server renders the UI and routes HTTP requests. It is safe for concurrent use:
// parsed templates and the mux are read-only after construction.
type Server struct {
	deps      ServerDeps
	mux       *http.ServeMux
	templates map[string]*template.Template
	static    http.Handler
	// oidc is the SSO protocol handler; nil when OIDC is not configured.
	oidc *oidcService
}

var _ http.Handler = (*Server)(nil)

// NewServer parses the embedded templates once and wires the routes. It returns
// an error if a template fails to parse, so a broken asset fails fast at startup
// rather than on first request.
func NewServer(deps ServerDeps) (*Server, error) {
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}
	server := &Server{
		deps:      deps,
		mux:       http.NewServeMux(),
		templates: templates,
		static:    http.StripPrefix(staticPrefix, http.FileServer(http.FS(staticRoot))),
	}
	if deps.OIDC.IssuerURL != "" {
		// Session and transaction cookies are meaningless without a signing
		// secret, so a missing one is a wiring bug worth failing on at startup.
		if len(deps.SessionSecret) == 0 {
			return nil, fmt.Errorf("web: OIDC is configured but no session secret was provided")
		}
		server.oidc = newOIDCService(deps.OIDC)
	}
	server.registerRoutes()
	return server, nil
}

// ServeHTTP satisfies http.Handler by delegating to the configured mux, first
// enforcing basic auth when credentials are configured.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Protect(s.mux).ServeHTTP(w, r)
}

// Protect wraps next with the same auth gate the Server applies to its own
// routes, for handlers mounted beside it (the SSE hub) that must not stay open
// when the UI is locked.
//
// With only basic auth configured the denial is exactly what it always was: a
// 401 with a WWW-Authenticate challenge so the browser prompts. With OIDC on,
// login pages replace browser dialogs: a document navigation is redirected to
// the login page, everything else (the live-poll fetch, EventSource, feeds)
// gets a plain 401 without a challenge so no basic-auth dialog fights the
// login flow — app.js turns that 401 into a redirect itself.
func (s *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		if s.oidcConfigured() {
			if isPublicPath(r) {
				next.ServeHTTP(w, r)
				return
			}
			if isDocumentRequest(r) {
				http.Redirect(w, r, loginPath, http.StatusFound)
				return
			}
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="sub_scribe", charset="UTF-8"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// authorized reports whether the request may proceed: always when no auth is
// configured at all; with a valid session cookie when OIDC is on; with the
// right basic-auth pair when credentials are set; or with a source's feed
// token, which authorizes its feed URL regardless of everything else.
func (s *Server) authorized(r *http.Request) bool {
	if !s.basicConfigured() && !s.oidcConfigured() {
		return true
	}
	if s.oidcConfigured() && s.hasValidSession(r) {
		return true
	}
	if s.basicConfigured() && s.basicAuthorized(r) {
		return true
	}
	return s.feedTokenAuthorized(r)
}

// basicConfigured reports whether HTTP basic auth credentials are set.
func (s *Server) basicConfigured() bool {
	return s.deps.Username != "" || s.deps.Password != ""
}

// basicAuthorized checks the request's basic-auth pair. Comparison is by
// constant-time digest so neither the timing nor the length of the configured
// secret leaks.
func (s *Server) basicAuthorized(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return digestEqual(user, s.deps.Username) && digestEqual(pass, s.deps.Password)
}

// feedTokenAuthorized grants a feed request carrying its source's capability
// token (/feeds/{id}?t=<token>). Podcast apps cannot complete a browser login,
// so the token in the subscribe URL is their whole authorization.
func (s *Server) feedTokenAuthorized(r *http.Request) bool {
	given := r.URL.Query().Get(feedTokenParam)
	if given == "" || r.Method != http.MethodGet {
		return false
	}
	idText, ok := strings.CutPrefix(r.URL.Path, feedPathPrefix)
	if !ok || strings.Contains(idText, "/") {
		return false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return false
	}
	source, err := s.deps.Sources.GetSource(r.Context(), id)
	if err != nil || source.FeedToken == "" {
		return false
	}
	return digestEqual(given, source.FeedToken)
}

// isPublicPath reports whether the request is one of the few endpoints that
// must work before login — the login page, the SSO round trip, logout, and
// the static assets the login page needs. Only consulted when OIDC is on.
func isPublicPath(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, staticPrefix) {
		return r.Method == http.MethodGet
	}
	switch r.URL.Path {
	case loginPath, oidcStartPath, oidcCallbackPath:
		return r.Method == http.MethodGet
	case logoutPath:
		return r.Method == http.MethodPost
	}
	return false
}

// isDocumentRequest distinguishes a top-level browser navigation — which
// should land on the login page — from fetch/EventSource/API traffic, which
// gets a plain 401. Sec-Fetch-Dest is authoritative where present; the Accept
// header is the fallback for older clients.
func isDocumentRequest(r *http.Request) bool {
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document"
	}
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// digestEqual compares two strings in constant time via their SHA-256 digests.
func digestEqual(got, want string) bool {
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

// parseTemplates builds one template set per page, each cloning its layout
// (the application chrome by default, the minimal auth layout for login) so
// pages may reuse block names without collision.
func parseTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template, len(pageNames))
	for _, page := range pageNames {
		layout := layoutFor(page)
		pageFile := "templates/" + page + ".html"
		parsed, err := template.New(layout).Funcs(templateFuncs()).
			ParseFS(templateFS, "templates/"+layout, pageFile)
		if err != nil {
			return nil, fmt.Errorf("web: parsing %s: %w", page, err)
		}
		templates[page] = parsed
	}
	return templates, nil
}

// render executes a page through the layout into a buffer first, so a template
// error yields a clean 500 instead of a half-written response.
func (s *Server) render(w http.ResponseWriter, page string, status int, data any) {
	parsed, ok := s.templates[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := parsed.ExecuteTemplate(&buf, layoutFor(page), data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
