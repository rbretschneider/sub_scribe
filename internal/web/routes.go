package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Route patterns use Go 1.22 method+pattern routing so each verb and path maps to
// exactly one handler with no manual method checks.
const (
	routeDashboard         = "GET /{$}"
	routeLibrary           = "GET /library"
	routeMediaDetail       = "GET /library/{id}"
	routeMediaRetry        = "POST /library/{id}/retry"
	routeMediaThumb        = "GET /library/{id}/thumb"
	routeJobs              = "GET /jobs"
	routeJobsClear         = "POST /jobs/clear"
	routeJobDetail         = "GET /jobs/{id}"
	routeJobRetry          = "POST /jobs/{id}/retry"
	routeJobDelete         = "POST /jobs/{id}/delete"
	routeSourcesList       = "GET /sources"
	routeSourceNew         = "GET /sources/new"
	routeSourceCreate      = "POST /sources"
	routeSourceDetail      = "GET /sources/{id}"
	routeSourceEdit        = "GET /sources/{id}/edit"
	routeSourceUpdate      = "POST /sources/{id}"
	routeSourceScan        = "POST /sources/{id}/scan"
	routeSourceRename      = "POST /sources/{id}/rename"
	routeSourceRetryFailed = "POST /sources/{id}/retry-failed"
	routeSourceEnabled     = "POST /sources/{id}/enabled"
	routeSourceDelete      = "POST /sources/{id}/delete"
	routeProfiles          = "GET /profiles"
	routeProfileNew        = "GET /profiles/new"
	routeProfileCreate     = "POST /profiles"
	routeProfileEdit       = "GET /profiles/{id}/edit"
	routeProfileUpdate     = "POST /profiles/{id}"
	routeProfileDelete     = "POST /profiles/{id}/delete"
	routeLogs              = "GET /logs"
	routeFeed              = "GET /feeds/{id}"
	routeTokenPage         = "GET /settings/token"
	routeTokenUpload       = "POST /settings/token"
	routeStatic            = "GET /static/"
)

// staticPrefix is stripped before the embedded file server resolves an asset.
const staticPrefix = "/static/"

// Navigation keys mark the active top-nav item so the current section is obvious.
const (
	navDashboard = "dashboard"
	navLibrary   = "library"
	navJobs      = "jobs"
	navSources   = "sources"
	navAddSource = "add-source"
	navProfiles  = "profiles"
	navLogs      = "logs"
	navToken     = "token"
)

// registerRoutes binds every handler to the mux.
func (s *Server) registerRoutes() {
	s.mux.HandleFunc(routeDashboard, s.handleDashboard)
	s.mux.HandleFunc(routeLibrary, s.handleLibrary)
	s.mux.HandleFunc(routeMediaDetail, s.handleMediaDetail)
	s.mux.HandleFunc(routeMediaRetry, s.handleMediaRetry)
	s.mux.HandleFunc(routeMediaThumb, s.handleMediaThumbnail)
	s.mux.HandleFunc(routeJobs, s.handleJobs)
	s.mux.HandleFunc(routeJobsClear, s.handleJobsClear)
	s.mux.HandleFunc(routeJobDetail, s.handleJobDetail)
	s.mux.HandleFunc(routeJobRetry, s.handleJobRetry)
	s.mux.HandleFunc(routeJobDelete, s.handleJobDelete)
	s.mux.HandleFunc(routeSourcesList, s.handleSources)
	s.mux.HandleFunc(routeSourceNew, s.handleSourceNew)
	s.mux.HandleFunc(routeSourceCreate, s.handleSourceCreate)
	s.mux.HandleFunc(routeSourceDetail, s.handleSourceDetail)
	s.mux.HandleFunc(routeSourceEdit, s.handleSourceEdit)
	s.mux.HandleFunc(routeSourceUpdate, s.handleSourceUpdate)
	s.mux.HandleFunc(routeSourceScan, s.handleSourceScan)
	s.mux.HandleFunc(routeSourceRename, s.handleSourceRename)
	s.mux.HandleFunc(routeSourceRetryFailed, s.handleSourceRetryFailed)
	s.mux.HandleFunc(routeSourceEnabled, s.handleSourceEnabled)
	s.mux.HandleFunc(routeSourceDelete, s.handleSourceDelete)
	s.mux.HandleFunc(routeProfiles, s.handleProfiles)
	s.mux.HandleFunc(routeProfileNew, s.handleProfileNew)
	s.mux.HandleFunc(routeProfileCreate, s.handleProfileCreate)
	s.mux.HandleFunc(routeProfileEdit, s.handleProfileEdit)
	s.mux.HandleFunc(routeProfileUpdate, s.handleProfileUpdate)
	s.mux.HandleFunc(routeProfileDelete, s.handleProfileDelete)
	s.mux.HandleFunc(routeLogs, s.handleLogs)
	s.mux.HandleFunc(routeFeed, s.handleFeed)
	s.mux.HandleFunc(routeTokenPage, s.handleTokenPage)
	s.mux.HandleFunc(routeTokenUpload, s.handleTokenUpload)
	s.mux.Handle(routeStatic, s.static)
}

// templateFuncs are the small, pure helpers the templates rely on for formatting.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"hoursOf":        durationHours,
		"daysOf":         durationDays,
		"selected":       selectedAttr,
		"formatDate":     formatDate,
		"formatDateTime": formatDateTime,
		"titleCase":      humanizeEnum,
		"jobStatusClass": jobStatusClass,
		"jobStatusLabel": jobStatusLabel,
		"jobTypeLabel":   jobTypeLabel,
		"cutoffWindow":   cutoffWindowLabel,
		"percentOf":      percentOf,
		"clock":          durationClock,
		"bytes":          bytesHuman,
		"statusClass":    statusClass,
		"statusLabel":    statusLabel,
		"thumb":          thumbGradient,
		"logTime":        logTimeFormat,
		"lower":          strings.ToLower,
		"libraryURL":     libraryURL,
		"retentionDelta": retentionDelta,
	}
}

// libraryURL builds a /library address carrying the given narrowing, omitting
// whatever is unset, so each filter link can preserve the others' state.
func libraryURL(status string, sourceID int64, search string) string {
	values := url.Values{}
	if status != "" {
		values.Set("status", status)
	}
	if sourceID > 0 {
		values.Set("source", strconv.FormatInt(sourceID, 10))
	}
	if search != "" {
		values.Set("q", search)
	}
	if len(values) == 0 {
		return "/library"
	}
	return "/library?" + values.Encode()
}

// baseView carries the fields the shared layout needs on every page.
type baseView struct {
	Title      string
	ActiveNav  string
	EventsPath string
	Token      tokenView
}

// newBaseView assembles the layout data, including the current token badge so it
// appears in the nav on every page.
func (s *Server) newBaseView(title, nav string) baseView {
	return baseView{
		Title:      title,
		ActiveNav:  nav,
		EventsPath: s.deps.EventsPath,
		Token:      s.assessToken(),
	}
}

// redirect issues a 303 See Other so a POST is not re-submitted on refresh.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}
