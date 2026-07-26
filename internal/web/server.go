// Package web is sub_scribe's server-rendered UI: an http.Handler that lists
// sources, drives the add-source flow, and makes YouTube-cookie setup trivial via
// a drag-and-drop token upload. Templates and static assets are embedded so the
// binary is fully self-contained, and the layer depends only on the library
// service interfaces and the cookies assessor, keeping it fakeable in tests.
package web

import (
	"bytes"
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
	Clock       jobs.Clock
	EventsPath  string
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

// ServeHTTP satisfies http.Handler by delegating to the configured mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
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
