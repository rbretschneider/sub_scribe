package web

import (
	"errors"
	"net/http"
	"strconv"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
	"sub_scribe/internal/store"
)

// jobsPageLimit caps how many queue entries the list loads at once. The queue can
// hold thousands of rows after a large channel is indexed; the screen shows the
// most relevant window rather than all of it.
const jobsPageLimit = 200

// jobLogLimit caps how many captured log lines a job's detail page shows.
const jobLogLimit = 500

// jobFilters maps a query-string filter to a queue status ("" = all).
var jobFilters = map[string]jobs.TaskStatus{
	"":          "",
	"running":   jobs.StatusRunning,
	"queued":    jobs.StatusPending,
	"failed":    jobs.StatusFailed,
	"succeeded": jobs.StatusSucceeded,
}

// jobsView is the render model for the queue list.
type jobsView struct {
	baseView
	Items  []library.JobListItem
	Filter string
	Counts map[string]int
	// Active is what is running or waiting; Finished is what the clear action
	// would remove.
	Active   int
	Finished int
}

// handleJobs renders the work queue: what is running now, what is waiting, and
// what failed, optionally filtered by ?status=.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("status")
	status, ok := jobFilters[filter]
	if !ok {
		filter, status = "", ""
	}

	items, err := s.deps.Jobs.ListJobs(r.Context(), library.JobFilter{Status: status, Limit: jobsPageLimit})
	if err != nil {
		http.Error(w, "could not load the job queue", http.StatusInternalServerError)
		return
	}
	counts, err := s.deps.Jobs.CountsByStatus(r.Context())
	if err != nil {
		http.Error(w, "could not load the job queue", http.StatusInternalServerError)
		return
	}

	view := jobsView{
		baseView: s.newBaseView(r, "Jobs", navJobs),
		Items:    items,
		Filter:   filter,
		Counts: map[string]int{
			"running":   counts[jobs.StatusRunning],
			"queued":    counts[jobs.StatusPending],
			"failed":    counts[jobs.StatusFailed],
			"succeeded": counts[jobs.StatusSucceeded],
		},
	}
	view.Active = view.Counts["running"] + view.Counts["queued"]
	view.Finished = view.Counts["succeeded"] + view.Counts["failed"]
	s.render(w, "jobs", http.StatusOK, view)
}

// jobDetailView is the render model for a single job, including the log lines it
// produced.
type jobDetailView struct {
	baseView
	Job      library.JobListItem
	Logs     []applog.Record
	CanRetry bool
	// CanDelete is false while the job is running, because its worker is still
	// going to report an outcome for it.
	CanDelete bool
	// WatchURL links a download job to the video on the provider, so a failing
	// item can be checked by hand in one click.
	WatchURL string
}

// handleJobDetail renders everything known about one job: what it is, what it
// acts on, how it ended, and the log output it produced.
func (s *Server) handleJobDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	job, err := s.deps.Jobs.GetJob(r.Context(), id)
	if err != nil {
		s.renderJobLookupError(w, err)
		return
	}

	view := jobDetailView{
		baseView:  s.newBaseView(r, "Job "+strconv.FormatInt(id, 10), navJobs),
		Job:       job,
		Logs:      s.deps.Logs.ForTask(id, jobLogLimit),
		CanRetry:  isFinished(job.Task.Status),
		CanDelete: job.Task.Status != jobs.StatusRunning,
		WatchURL:  watchURLFor(job.MediaExternalID),
	}
	s.render(w, "job_detail", http.StatusOK, view)
}

// handleJobRetry requeues a finished job and returns to its detail page, so the
// user sees the new status rather than a bare redirect to the list.
func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	if err := s.deps.Queue.Retry(r.Context(), id, s.deps.Clock.Now()); err != nil {
		if errors.Is(err, library.ErrJobNotRetryable) {
			// Already queued or running — the state the user asked for. Its page
			// shows that; a bare conflict error would be a dead end.
			redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10))
			return
		}
		s.renderJobLookupError(w, err)
		return
	}
	redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10))
}

// handleJobDelete removes one queue entry and returns to the list, since the
// page the user was on no longer exists.
func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	if err := s.deps.Queue.Delete(r.Context(), id); err != nil {
		if errors.Is(err, library.ErrJobNotDeletable) {
			// Back to the job, whose page shows it is running and withholds the
			// delete button — clearer than a bare conflict error page.
			redirect(w, r, "/jobs/"+strconv.FormatInt(id, 10))
			return
		}
		http.Error(w, "could not delete that job", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/jobs")
}

// handleJobsClear removes every finished queue entry, leaving queued and running
// work untouched.
func (s *Server) handleJobsClear(w http.ResponseWriter, r *http.Request) {
	if _, err := s.deps.Queue.DeleteFinished(r.Context()); err != nil {
		http.Error(w, "could not clear finished jobs", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/jobs")
}

// renderJobLookupError answers 404 for a job that does not exist and 500 for a
// genuine failure, so a stale link never looks like a broken server.
func (s *Server) renderJobLookupError(w http.ResponseWriter, err error) {
	if store.IsNotFound(err) {
		http.Error(w, "no such job", http.StatusNotFound)
		return
	}
	http.Error(w, "could not load that job", http.StatusInternalServerError)
}

// isFinished reports whether a job has settled and can therefore be retried.
func isFinished(status jobs.TaskStatus) bool {
	return status == jobs.StatusFailed || status == jobs.StatusSucceeded
}

// parseIDParam reads the {id} path value as an integer, writing a 400 and
// reporting false when it is missing or malformed.
func parseIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
