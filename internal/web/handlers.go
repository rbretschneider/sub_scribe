package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// dashboardView is the render model for the overview page. Counts are flattened
// to named fields so the template stays free of map lookups.
type dashboardView struct {
	baseView
	Sources        int
	Archived       int
	Downloading    int
	Queued         int
	Failed         int
	Storage        string
	NowDownloading []library.MediaListItem
	UpNext         []library.MediaListItem
	Recent         []library.MediaListItem
}

// handleDashboard renders the overview: headline counts, in-flight downloads, the
// queue, and recently archived media, plus the token health badge.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	overview, err := s.deps.Library.Overview(r.Context())
	if err != nil {
		http.Error(w, "could not load your archive", http.StatusInternalServerError)
		return
	}
	view := dashboardView{
		baseView:       s.newBaseView("Dashboard", navDashboard),
		Sources:        overview.SourceCount,
		Archived:       overview.Counts[domain.MediaDownloaded],
		Downloading:    overview.Counts[domain.MediaDownloading],
		Queued:         overview.Counts[domain.MediaPending],
		Failed:         overview.Counts[domain.MediaFailed],
		Storage:        bytesHuman(overview.StorageBytes),
		NowDownloading: overview.Downloading,
		UpNext:         overview.Queued,
		Recent:         overview.Recent,
	}
	s.render(w, "dashboard", http.StatusOK, view)
}

// libraryView is the render model for the media library grid.
type libraryView struct {
	baseView
	Items  []library.MediaListItem
	Total  int
	Filter string
	Counts map[string]int
}

// libraryFilters maps a query-string filter to a media status ("" = all).
var libraryFilters = map[string]domain.MediaStatus{
	"":            "",
	"downloaded":  domain.MediaDownloaded,
	"downloading": domain.MediaDownloading,
	"queued":      domain.MediaPending,
	"failed":      domain.MediaFailed,
	"unavailable": domain.MediaUnavailable,
	"skipped":     domain.MediaSkipped,
}

// libraryPageLimit caps how many videos the library grid loads at once.
const libraryPageLimit = 120

// handleLibrary renders the grid of archived videos, optionally filtered by status
// via the ?status= query parameter.
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("status")
	status, ok := libraryFilters[filter]
	if !ok {
		filter, status = "", ""
	}

	items, err := s.deps.Library.ListMedia(r.Context(), status, libraryPageLimit)
	if err != nil {
		http.Error(w, "could not load your library", http.StatusInternalServerError)
		return
	}
	overview, err := s.deps.Library.Overview(r.Context())
	if err != nil {
		http.Error(w, "could not load your library", http.StatusInternalServerError)
		return
	}
	view := libraryView{
		baseView: s.newBaseView("Library", navLibrary),
		Items:    items,
		Total:    overview.TotalMedia,
		Filter:   filter,
		Counts: map[string]int{
			"downloaded":  overview.Counts[domain.MediaDownloaded],
			"downloading": overview.Counts[domain.MediaDownloading],
			"queued":      overview.Counts[domain.MediaPending],
			"failed":      overview.Counts[domain.MediaFailed],
			"unavailable": overview.Counts[domain.MediaUnavailable],
			"skipped":     overview.Counts[domain.MediaSkipped],
		},
	}
	s.render(w, "library", http.StatusOK, view)
}

// logsView is the render model for the log viewer.
type logsView struct {
	baseView
	Records []applog.Record
}

// logsPageLimit caps how many recent log records the viewer shows.
const logsPageLimit = 500

// handleLogs renders the most recent application log records, newest first.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	view := logsView{
		baseView: s.newBaseView("Logs", navLogs),
		Records:  s.deps.Logs.Recent(logsPageLimit),
	}
	s.render(w, "logs", http.StatusOK, view)
}

// sourcesView is the render model for the sources management page. Stats are
// keyed by source id so the delete confirmation can state exactly how many files
// and how much disk are at stake.
type sourcesView struct {
	baseView
	Sources []domain.Source
	Stats   map[int64]library.SourceStats
	// LargestBytes is the biggest source's size, used to scale the usage bars so
	// the heaviest source is obvious without comparing numbers. Zero when nothing
	// has been downloaded yet, which hides the bars entirely.
	LargestBytes int64
}

// handleSources lists the tracked sources for management (edit, delete, add).
func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.deps.Sources.ListSources(r.Context())
	if err != nil {
		http.Error(w, "could not load sources", http.StatusInternalServerError)
		return
	}
	stats, err := s.deps.Library.SourceStats(r.Context())
	if err != nil {
		http.Error(w, "could not load sources", http.StatusInternalServerError)
		return
	}
	view := sourcesView{
		baseView:     s.newBaseView("Sources", navSources),
		Sources:      sources,
		Stats:        stats,
		LargestBytes: largestSourceBytes(stats),
	}
	s.render(w, "sources", http.StatusOK, view)
}

// largestSourceBytes returns the size of the biggest source, or zero when
// nothing has been downloaded.
func largestSourceBytes(stats map[int64]library.SourceStats) int64 {
	var largest int64
	for _, entry := range stats {
		if entry.Bytes > largest {
			largest = entry.Bytes
		}
	}
	return largest
}

// sourceFormView is the render model for the source form, carrying the profile
// options, any validation error, the user's entered values, and the form's
// heading/action/label so one template serves both create and edit.
type sourceFormView struct {
	baseView
	Profiles    []domain.MediaProfile
	Error       string
	Values      sourceFormValues
	Heading     string
	Action      string
	SubmitLabel string
}

// sourceFormOptions configures a source-form render, keeping renderSourceForm to
// a single parameter object rather than a long argument list.
type sourceFormOptions struct {
	Heading     string
	Action      string
	SubmitLabel string
	Status      int
	Values      sourceFormValues
	Message     string
}

// handleSourceNew renders the empty add-source form.
func (s *Server) handleSourceNew(w http.ResponseWriter, r *http.Request) {
	s.renderSourceForm(w, r.Context(), sourceFormOptions{
		Heading:     "Add a source",
		Action:      "/sources",
		SubmitLabel: "Add source",
		Status:      http.StatusOK,
	})
}

// handleSourceCreate validates the submitted form and creates the source. On a
// validation error it re-renders the form with the message and entered values; on
// success it redirects home so a refresh cannot double-submit.
func (s *Server) handleSourceCreate(w http.ResponseWriter, r *http.Request) {
	values := readFormValues(r)
	opts := sourceFormOptions{Heading: "Add a source", Action: "/sources", SubmitLabel: "Add source", Status: http.StatusOK, Values: values}
	input, err := values.toInput()
	if err != nil {
		opts.Message = err.Error()
		s.renderSourceForm(w, r.Context(), opts)
		return
	}
	if _, err := s.deps.Sources.AddSource(r.Context(), input); err != nil {
		opts.Message = "We couldn't save this source. Please try again."
		s.renderSourceForm(w, r.Context(), opts)
		return
	}
	redirect(w, r, "/")
}

// handleSourceEdit renders the form pre-filled with an existing source's values.
func (s *Server) handleSourceEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	source, err := s.deps.Sources.GetSource(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderSourceForm(w, r.Context(), sourceFormOptions{
		Heading:     "Edit source",
		Action:      fmt.Sprintf("/sources/%d", id),
		SubmitLabel: "Save changes",
		Status:      http.StatusOK,
		Values:      fromSource(source),
	})
}

// handleSourceUpdate validates the submitted form and saves changes to an
// existing source, then returns to that source's detail page.
func (s *Server) handleSourceUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	values := readFormValues(r)
	opts := sourceFormOptions{
		Heading:     "Edit source",
		Action:      fmt.Sprintf("/sources/%d", id),
		SubmitLabel: "Save changes",
		Status:      http.StatusOK,
		Values:      values,
	}
	input, err := values.toInput()
	if err != nil {
		opts.Message = err.Error()
		s.renderSourceForm(w, r.Context(), opts)
		return
	}
	if _, err := s.deps.Sources.UpdateSource(r.Context(), id, input); err != nil {
		opts.Message = "We couldn't save your changes. Please try again."
		s.renderSourceForm(w, r.Context(), opts)
		return
	}
	redirect(w, r, fmt.Sprintf("/sources/%d", id))
}

// renderSourceForm loads the profile options and renders the form for the given
// mode (create or edit).
func (s *Server) renderSourceForm(w http.ResponseWriter, ctx context.Context, opts sourceFormOptions) {
	profiles, err := s.deps.Profiles.ListProfiles(ctx)
	if err != nil {
		http.Error(w, "could not load profiles", http.StatusInternalServerError)
		return
	}
	// Preselect the first profile on a blank form. There is always at least one
	// (seeded on first run) and it is already set up for media servers, so an
	// empty "choose a profile" placeholder only invites a validation error.
	if opts.Values.MediaProfileID == "" && len(profiles) > 0 {
		opts.Values.MediaProfileID = strconv.FormatInt(profiles[0].ID, 10)
	}

	view := sourceFormView{
		baseView:    s.newBaseView(opts.Heading, navSources),
		Profiles:    profiles,
		Error:       opts.Message,
		Values:      opts.Values,
		Heading:     opts.Heading,
		Action:      opts.Action,
		SubmitLabel: opts.SubmitLabel,
	}
	s.render(w, "source_form", opts.Status, view)
}

// sourceDetailView is the render model for a single source page.
type sourceDetailView struct {
	baseView
	Source domain.Source
	Stats  library.SourceStats
}

// handleSourceDetail shows one source, or 404 if it does not exist.
func (s *Server) handleSourceDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	source, err := s.deps.Sources.GetSource(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	stats, err := s.deps.Library.SourceStats(r.Context())
	if err != nil {
		http.Error(w, "could not load that source", http.StatusInternalServerError)
		return
	}
	view := sourceDetailView{
		baseView: s.newBaseView(source.Name, navSources),
		Source:   source,
		Stats:    stats[id],
	}
	s.render(w, "source_detail", http.StatusOK, view)
}

// handleSourceScan forces an immediate, out-of-schedule scan of a source and
// returns to its detail page.
func (s *Server) handleSourceScan(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Sources.RequestScan(r.Context(), id); err != nil {
		http.Error(w, "could not start a scan", http.StatusInternalServerError)
		return
	}
	redirect(w, r, fmt.Sprintf("/sources/%d", id))
}

// enabledFormField carries the desired state for the pause/resume control.
const enabledFormField = "enabled"

// handleSourceEnabled pauses or resumes a source. The desired state is submitted
// explicitly rather than flipped from whatever is stored, so a double submit or a
// stale page cannot leave the source in the opposite state to the button the user
// actually pressed.
func (s *Server) handleSourceEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	enabled := r.PostFormValue(enabledFormField) == "true"
	if err := s.deps.Sources.SetSourceEnabled(r.Context(), id, enabled); err != nil {
		http.Error(w, "could not change whether this source is active", http.StatusInternalServerError)
		return
	}
	redirect(w, r, redirectTargetOr(r, fmt.Sprintf("/sources/%d", id)))
}

// returnToFormField lets a control tell the server which page to return to, so
// pausing from the list goes back to the list and pausing from the detail page
// stays on the detail page.
const returnToFormField = "return_to"

// redirectTargetOr returns the submitted return path when it is a safe in-app
// path, and fallback otherwise. Only a rooted, non-protocol-relative path is
// accepted, so this can never be turned into an open redirect.
func redirectTargetOr(r *http.Request, fallback string) string {
	target := r.PostFormValue(returnToFormField)
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return target
	}
	return fallback
}

// deleteFilesFormField opts a deletion in to removing the media files too.
const deleteFilesFormField = "delete_files"

// handleSourceDelete removes a source and returns to the dashboard. Deleting the
// downloaded files is opt-in: the field must be present and explicitly true, so
// anything else — a stale form, a missing checkbox, a scripted request — leaves
// the media on disk.
func (s *Server) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	opts := library.DeleteSourceOptions{
		DeleteFiles: r.PostFormValue(deleteFilesFormField) == "true",
	}
	if err := s.deps.Sources.DeleteSource(r.Context(), id, opts); err != nil {
		http.Error(w, "could not delete source", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/sources")
}

// parseID reads the {id} path value, writing a 404 for a non-numeric id.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}
