package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// fakeJobs is an in-memory JobReader and QueueMaintain for the queue screens.
type fakeJobs struct {
	items   []library.JobListItem
	counts  map[jobs.TaskStatus]int
	getErr  error
	filters []library.JobFilter

	retried  []int64
	retryErr error

	deleted         []int64
	deleteErr       error
	clearedFinished bool
}

func (f *fakeJobs) ListJobs(_ context.Context, filter library.JobFilter) ([]library.JobListItem, error) {
	f.filters = append(f.filters, filter)
	if filter.Status == "" {
		return f.items, nil
	}
	var matched []library.JobListItem
	for _, item := range f.items {
		if item.Task.Status == filter.Status {
			matched = append(matched, item)
		}
	}
	return matched, nil
}

func (f *fakeJobs) GetJob(_ context.Context, id int64) (library.JobListItem, error) {
	if f.getErr != nil {
		return library.JobListItem{}, f.getErr
	}
	for _, item := range f.items {
		if item.Task.ID == id {
			return item, nil
		}
	}
	return library.JobListItem{}, sql.ErrNoRows
}

func (f *fakeJobs) CountsByStatus(context.Context) (map[jobs.TaskStatus]int, error) {
	return f.counts, nil
}

func (f *fakeJobs) Retry(_ context.Context, id int64, _ time.Time) error {
	if f.retryErr != nil {
		return f.retryErr
	}
	f.retried = append(f.retried, id)
	return nil
}

func (f *fakeJobs) Delete(_ context.Context, id int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeJobs) DeleteFinished(context.Context) (int, error) {
	f.clearedFinished = true
	return f.counts[jobs.StatusSucceeded] + f.counts[jobs.StatusFailed], nil
}

func (f *fakeJobs) DeleteFinishedBefore(context.Context, time.Time) (int, error) { return 0, nil }
func (f *fakeJobs) DeleteOrphans(context.Context) (int, error)                   { return 0, nil }
func (f *fakeJobs) RequeueRunning(context.Context, time.Time) (int, error)       { return 0, nil }
func (f *fakeJobs) ActiveMediaIDs(context.Context) (map[int64]bool, error)       { return nil, nil }
func (f *fakeJobs) RetryAllFailed(_ context.Context, _ int64, _ time.Time, _ time.Time) (int, error) {
	return 0, nil
}

// fakeMediaService records retry requests from the media detail screen.
type fakeMediaService struct {
	retried []int64
	err     error
}

func (f *fakeMediaService) RetryMedia(_ context.Context, id int64) error {
	if f.err != nil {
		return f.err
	}
	f.retried = append(f.retried, id)
	return nil
}

// queueServer builds a Server wired with the queue fakes and a log buffer.
func queueServer(t *testing.T, jobsFake *fakeJobs, lib library.LibraryReader, media library.MediaService, logs LogReader) *Server {
	t.Helper()
	if logs == nil {
		logs = applog.NewBuffer(0)
	}
	server, err := NewServer(ServerDeps{
		Sources:     &fakeSources{},
		Profiles:    &fakeProfiles{},
		Library:     lib,
		Media:       media,
		Jobs:        jobsFake,
		Queue:       jobsFake,
		Logs:        logs,
		CookiesPath: filepath.Join(t.TempDir(), "cookies.txt"),
		Clock:       fixedClock{now: testNow},
		EventsPath:  "/events",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

// downloadJob builds a queue entry for a download of one video.
func downloadJob(id int64, status jobs.TaskStatus) library.JobListItem {
	sourceID, mediaID := int64(7), int64(99)
	return library.JobListItem{
		Task: jobs.Task{
			ID: id, Type: jobs.TaskDownloadMedia, Status: status,
			MediaID: &mediaID, Attempts: 1, MaxAttempts: 3,
			CreatedAt: testNow, UpdatedAt: testNow, RunAfter: testNow,
		},
		SourceID: &sourceID, SourceName: "Realistick",
		MediaID: &mediaID, MediaTitle: "Toyota Supra MT", MediaExternalID: "gCZOjDar1tU",
	}
}

// get performs a GET and returns the recorder.
func get(server *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestJobsPageListsQueueEntriesAndLinksToEach(t *testing.T) {
	fake := &fakeJobs{
		items:  []library.JobListItem{downloadJob(12, jobs.StatusRunning)},
		counts: map[jobs.TaskStatus]int{jobs.StatusRunning: 1, jobs.StatusPending: 4},
	}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Download Media", "Toyota Supra MT", "Realistick", `href="/jobs/12"`} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs page missing %q", want)
		}
	}
	if !strings.Contains(body, "Queued · 4") {
		t.Error("jobs page does not show the queued count")
	}
}

func TestJobsPageFiltersByStatus(t *testing.T) {
	fake := &fakeJobs{
		items: []library.JobListItem{
			downloadJob(1, jobs.StatusFailed),
			downloadJob(2, jobs.StatusSucceeded),
		},
		counts: map[jobs.TaskStatus]int{},
	}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs?status=failed")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := fake.filters[0].Status; got != jobs.StatusFailed {
		t.Errorf("filter status = %q, want %q", got, jobs.StatusFailed)
	}
	if !strings.Contains(rec.Body.String(), `href="/jobs/1"`) {
		t.Error("filtered page should list the failed job")
	}
	if strings.Contains(rec.Body.String(), `href="/jobs/2"`) {
		t.Error("filtered page should not list the succeeded job")
	}
}

func TestJobDetailShowsTheErrorAndTheJobsOwnLogLines(t *testing.T) {
	failed := downloadJob(12, jobs.StatusFailed)
	failed.Task.LastError = `yt-dlp download "https://youtu.be/x": exit status 1: ERROR: members-only`

	logs := applog.NewBuffer(0)
	logs.Append(applog.Record{Time: testNow, Level: "ERROR", Message: "yt-dlp: ERROR: members-only", TaskID: 12})
	logs.Append(applog.Record{Time: testNow, Level: "INFO", Message: "unrelated line", TaskID: 99})

	fake := &fakeJobs{items: []library.JobListItem{failed}, counts: map[jobs.TaskStatus]int{}}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, logs), "/jobs/12")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "members-only") {
		t.Error("job detail does not show the failure reason")
	}
	if !strings.Contains(body, "yt-dlp: ERROR: members-only") {
		t.Error("job detail does not show the job's log output")
	}
	if strings.Contains(body, "unrelated line") {
		t.Error("job detail leaked another job's log lines")
	}
	// The targets must be reachable, which is the whole point of the screen.
	for _, want := range []string{`href="/sources/7"`, `href="/library/99"`, "gCZOjDar1tU"} {
		if !strings.Contains(body, want) {
			t.Errorf("job detail missing link or id %q", want)
		}
	}
}

func TestJobDetailOffersRetryOnlyForFinishedJobs(t *testing.T) {
	tests := []struct {
		name      string
		status    jobs.TaskStatus
		wantRetry bool
	}{
		{"failed job can be run again", jobs.StatusFailed, true},
		{"succeeded job can be run again", jobs.StatusSucceeded, true},
		{"running job cannot", jobs.StatusRunning, false},
		{"queued job cannot", jobs.StatusPending, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeJobs{
				items:  []library.JobListItem{downloadJob(3, test.status)},
				counts: map[jobs.TaskStatus]int{},
			}
			rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs/3")

			hasRetry := strings.Contains(rec.Body.String(), `action="/jobs/3/retry"`)
			if hasRetry != test.wantRetry {
				t.Errorf("retry button present = %v, want %v", hasRetry, test.wantRetry)
			}
		})
	}
}

func TestJobDetailIsNotFoundForAnUnknownJob(t *testing.T) {
	fake := &fakeJobs{counts: map[jobs.TaskStatus]int{}}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs/404")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestJobRetryRequeuesAndReturnsToTheJob(t *testing.T) {
	fake := &fakeJobs{
		items:  []library.JobListItem{downloadJob(12, jobs.StatusFailed)},
		counts: map[jobs.TaskStatus]int{},
	}
	server := queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/12/retry", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/jobs/12" {
		t.Errorf("Location = %q, want /jobs/12", got)
	}
	if len(fake.retried) != 1 || fake.retried[0] != 12 {
		t.Errorf("retried = %v, want [12]", fake.retried)
	}
}

func TestJobRetryReportsAConflictWhenTheJobIsStillQueued(t *testing.T) {
	fake := &fakeJobs{counts: map[jobs.TaskStatus]int{}, retryErr: library.ErrJobNotRetryable}
	server := queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/12/retry", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestMediaDetailShowsTheFullErrorAndOffersARetry(t *testing.T) {
	lib := &fakeLibrary{item: library.MediaListItem{
		SourceName: "Realistick",
		Media: domain.Media{
			ID: 99, SourceID: 7, ExternalID: "gCZOjDar1tU", Status: domain.MediaFailed,
			LastError: "ERROR: Unable to rename file: no such file or directory",
			Metadata:  domain.MediaMetadata{Title: "Toyota Supra MT"},
		},
	}}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library/99")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Toyota Supra MT",
		"Unable to rename file",
		`action="/library/99/retry"`,
		"https://www.youtube.com/watch?v=gCZOjDar1tU",
		`href="/sources/7"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("media detail missing %q", want)
		}
	}
}

func TestMediaDetailHidesRetryWhileADownloadIsAlreadyQueued(t *testing.T) {
	lib := &fakeLibrary{item: library.MediaListItem{
		Media: domain.Media{ID: 99, Status: domain.MediaDownloading},
	}}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library/99")

	if strings.Contains(rec.Body.String(), `action="/library/99/retry"`) {
		t.Error("retry offered while the item is already downloading")
	}
}

func TestMediaRetryQueuesTheDownloadAgain(t *testing.T) {
	media := &fakeMediaService{}
	server := queueServer(t, &fakeJobs{}, &fakeLibrary{}, media, nil)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/library/99/retry", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(media.retried) != 1 || media.retried[0] != 99 {
		t.Errorf("retried = %v, want [99]", media.retried)
	}
}

func TestJobDeleteRemovesTheEntryAndReturnsToTheList(t *testing.T) {
	fake := &fakeJobs{
		items:  []library.JobListItem{downloadJob(12, jobs.StatusFailed)},
		counts: map[jobs.TaskStatus]int{},
	}
	server := queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/12/delete", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/jobs" {
		t.Errorf("Location = %q, want /jobs (the job's page is gone)", got)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != 12 {
		t.Errorf("deleted = %v, want [12]", fake.deleted)
	}
}

func TestJobDeleteRefusesARunningJob(t *testing.T) {
	fake := &fakeJobs{counts: map[jobs.TaskStatus]int{}, deleteErr: library.ErrJobNotDeletable}
	server := queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/12/delete", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestJobDetailHidesDeleteWhileTheJobIsRunning(t *testing.T) {
	fake := &fakeJobs{
		items:  []library.JobListItem{downloadJob(3, jobs.StatusRunning)},
		counts: map[jobs.TaskStatus]int{},
	}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs/3")

	if strings.Contains(rec.Body.String(), `action="/jobs/3/delete"`) {
		t.Error("delete offered for a running job, whose worker will still report an outcome")
	}
}

func TestClearFinishedRemovesSettledJobsOnly(t *testing.T) {
	fake := &fakeJobs{counts: map[jobs.TaskStatus]int{
		jobs.StatusSucceeded: 40, jobs.StatusFailed: 2, jobs.StatusPending: 5,
	}}
	server := queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil)

	// The button is offered with a count so the user knows what they are clearing.
	listing := get(server, "/jobs")
	if !strings.Contains(listing.Body.String(), "Clear finished (42)") {
		t.Error("jobs page does not offer to clear the 42 finished jobs")
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/clear", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if !fake.clearedFinished {
		t.Error("finished jobs were not cleared")
	}
}

func TestClearFinishedIsNotOfferedWhenThereIsNoHistory(t *testing.T) {
	fake := &fakeJobs{counts: map[jobs.TaskStatus]int{jobs.StatusPending: 3}}
	rec := get(queueServer(t, fake, &fakeLibrary{}, &fakeMediaService{}, nil), "/jobs")

	if strings.Contains(rec.Body.String(), "Clear finished") {
		t.Error("clear offered with nothing to clear")
	}
}

func TestLibraryExposesEveryStatusItCanRecord(t *testing.T) {
	// A status the library cannot filter to is a status the user cannot see. Most
	// of a large channel can end up skipped by the date cutoff, and that has to be
	// discoverable rather than silently missing from the totals.
	lib := &fakeLibrary{overview: library.Overview{
		TotalMedia: 624,
		Counts: map[domain.MediaStatus]int{
			domain.MediaDownloaded: 3, domain.MediaSkipped: 584, domain.MediaFailed: 37,
		},
	}}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library")

	body := rec.Body.String()
	for _, want := range []string{"Skipped · 584", "Failed · 37", "Downloaded · 3"} {
		if !strings.Contains(body, want) {
			t.Errorf("library page missing %q", want)
		}
	}
	// The headline counts everything tracked, so it must not claim they are archived.
	if strings.Contains(body, "624 videos archived") {
		t.Error("headline calls tracked videos archived; only 3 were downloaded")
	}
}

func TestLibraryFiltersToSkipped(t *testing.T) {
	lib := &fakeLibrary{
		overview: library.Overview{Counts: map[domain.MediaStatus]int{domain.MediaSkipped: 2}},
		media: []library.MediaListItem{{
			SourceName: "msrachel",
			Media:      domain.Media{ID: 5, Status: domain.MediaSkipped},
		}},
	}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library?status=skipped")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "published within") {
		t.Error("the skipped view should explain why these were not downloaded")
	}
}

func TestThumbnailIsServedFromBesideTheVideo(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Chan - 2026-07-16 - A Video [abc123].mkv")
	if err := os.WriteFile(video, []byte("video"), 0o600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	// yt-dlp writes the artwork sharing the video's base name — the layout Plex
	// reads as episode art.
	if err := os.WriteFile(filepath.Join(dir, "Chan - 2026-07-16 - A Video [abc123].jpg"),
		[]byte("jpegdata"), 0o600); err != nil {
		t.Fatalf("write thumb: %v", err)
	}

	lib := &fakeLibrary{item: library.MediaListItem{
		Media: domain.Media{ID: 5, Status: domain.MediaDownloaded, FilePath: video},
	}}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library/5/thumb")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if rec.Body.String() != "jpegdata" {
		t.Errorf("body = %q, want the thumbnail bytes", rec.Body.String())
	}
}

func TestMissingThumbnailIsANotFoundSoTheGridFallsBack(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "no-art.mkv")
	if err := os.WriteFile(video, []byte("video"), 0o600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	lib := &fakeLibrary{item: library.MediaListItem{
		Media: domain.Media{ID: 5, Status: domain.MediaDownloaded, FilePath: video},
	}}

	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library/5/thumb")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestThumbnailPathIsNeverTakenFromTheRequest(t *testing.T) {
	// The served path is derived from the recorded file, so an id is all the
	// caller controls — there is no filename in the URL to traverse with.
	lib := &fakeLibrary{item: library.MediaListItem{
		Media: domain.Media{ID: 5, Status: domain.MediaDownloaded, FilePath: ""},
	}}
	server := queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil)

	for _, path := range []string{
		"/library/5/thumb",   // recorded path is empty: nothing to serve
		"/library/abc/thumb", // malformed id
	} {
		rec := get(server, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s unexpectedly served content (%d)", path, rec.Code)
		}
	}
}

func TestLibraryGridRequestsThumbnailsOnlyForDownloadedItems(t *testing.T) {
	lib := &fakeLibrary{
		overview: library.Overview{Counts: map[domain.MediaStatus]int{}},
		media: []library.MediaListItem{
			{SourceName: "Chan", Media: domain.Media{ID: 7, Status: domain.MediaDownloaded, FilePath: "/media/a.mkv"}},
			{SourceName: "Chan", Media: domain.Media{ID: 8, Status: domain.MediaSkipped}},
		},
	}
	rec := get(queueServer(t, &fakeJobs{}, lib, &fakeMediaService{}, nil), "/library")

	body := rec.Body.String()
	if !strings.Contains(body, `src="/library/7/thumb"`) {
		t.Error("downloaded item should request its thumbnail")
	}
	if strings.Contains(body, `src="/library/8/thumb"`) {
		t.Error("an item with no file should not request a thumbnail")
	}
}

func TestQueueRoutesRejectAMalformedID(t *testing.T) {
	server := queueServer(t, &fakeJobs{counts: map[jobs.TaskStatus]int{}}, &fakeLibrary{}, &fakeMediaService{}, nil)

	for _, path := range []string{"/jobs/abc", "/library/abc"} {
		rec := get(server, path)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}
