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

// pageNames are the content templates, each composed with the layout at startup
// so a "content" block from one page never collides with another's.
var pageNames = []string{
	"dashboard", "library", "media_detail", "source_form", "source_detail",
	"token", "profiles", "profile_form", "sources", "logs", "jobs", "job_detail",
	"download_form",
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
}

// Server renders the UI and routes HTTP requests. It is safe for concurrent use:
// parsed templates and the mux are read-only after construction.
type Server struct {
	deps      ServerDeps
	mux       *http.ServeMux
	templates map[string]*template.Template
	static    http.Handler
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
	server.registerRoutes()
	return server, nil
}

// ServeHTTP satisfies http.Handler by delegating to the configured mux, first
// enforcing basic auth when credentials are configured.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Protect(s.mux).ServeHTTP(w, r)
}

// Protect wraps next with the same basic-auth gate the Server applies to its
// own routes, for handlers mounted beside it (the SSE hub) that must not stay
// open when the UI is locked.
func (s *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="sub_scribe", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorized reports whether the request may proceed: always when no
// credentials are configured, otherwise only with the right basic-auth pair.
// Comparison is by constant-time digest so neither the timing nor the length
// of the configured secret leaks.
func (s *Server) authorized(r *http.Request) bool {
	if s.deps.Username == "" && s.deps.Password == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return digestEqual(user, s.deps.Username) && digestEqual(pass, s.deps.Password)
}

// digestEqual compares two strings in constant time via their SHA-256 digests.
func digestEqual(got, want string) bool {
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

// parseTemplates builds one template set per page, each cloning the shared layout
// so pages may reuse block names without collision.
func parseTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template, len(pageNames))
	for _, page := range pageNames {
		pageFile := "templates/" + page + ".html"
		parsed, err := template.New(layoutTemplate).Funcs(templateFuncs()).
			ParseFS(templateFS, "templates/"+layoutTemplate, pageFile)
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
	if err := parsed.ExecuteTemplate(&buf, layoutTemplate, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
