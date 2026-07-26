package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// queueFixture is a database seeded with one source, one media item, and the
// repositories under test — the minimum shape every queue read needs.
type queueFixture struct {
	db       *DB
	tasks    *TaskRepo
	media    *MediaRepo
	sourceID int64
	mediaID  int64
}

// orphanMedia deletes a media row while leaving its tasks behind, reproducing
// the inconsistent state that storage which loses writes leaves on disk. Foreign
// keys are switched off for the delete because the schema would otherwise
// cascade the tasks away — which is exactly what did not happen in the wild.
func (f queueFixture) orphanMedia(t *testing.T, mediaID int64) {
	t.Helper()
	if _, err := f.db.sql.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := f.db.sql.Exec(`DELETE FROM media WHERE id = ?`, mediaID); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	if _, err := f.db.sql.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("re-enable foreign keys: %v", err)
	}
}

func newQueueFixture(t *testing.T) queueFixture {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	profileID, err := db.Profiles().Create(ctx, domain.MediaProfile{
		Name: "P", OutputPathTemplate: "{{ title }}", Kind: domain.MediaVideo,
		MetadataFormat: domain.MetadataPlex, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	sourceID, err := db.Sources().Create(ctx, domain.Source{
		Name: "Realistick", URL: "https://youtube.com/@Realistick",
		CollectionType: domain.CollectionChannel, MediaProfileID: profileID,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	mediaID, err := db.Media().Upsert(ctx, domain.Media{
		SourceID: sourceID, ExternalID: "vid1", Status: domain.MediaPending,
		Metadata:  domain.MediaMetadata{Title: "A Great Video"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert media: %v", err)
	}
	return queueFixture{
		db: db, tasks: db.Tasks(), media: db.Media(),
		sourceID: sourceID, mediaID: mediaID,
	}
}

func TestListJobsResolvesTheChannelThroughTheMediaItem(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	// A download task carries only a media id — the channel has to be reached
	// through the media row, or the queue screen shows a nameless job.
	if _, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := f.tasks.ListJobs(ctx, library.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ListJobs returned %d items, want 1", len(items))
	}

	item := items[0]
	if item.SourceName != "Realistick" {
		t.Errorf("SourceName = %q, want %q", item.SourceName, "Realistick")
	}
	if item.SourceID == nil || *item.SourceID != f.sourceID {
		t.Errorf("SourceID = %v, want %d", item.SourceID, f.sourceID)
	}
	if item.MediaTitle != "A Great Video" {
		t.Errorf("MediaTitle = %q, want %q", item.MediaTitle, "A Great Video")
	}
	if item.MediaExternalID != "vid1" {
		t.Errorf("MediaExternalID = %q, want %q", item.MediaExternalID, "vid1")
	}
}

func TestListJobsFiltersByStatusAndOrdersActiveWorkFirst(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	queuedID, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now).ForSource(f.sourceID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	doneID, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now).ForSource(f.sourceID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := f.tasks.Complete(ctx, doneID, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	all, err := f.tasks.ListJobs(ctx, library.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 2 || all[0].Task.ID != queuedID {
		t.Fatalf("expected the queued job first, got %+v", all)
	}

	succeeded, err := f.tasks.ListJobs(ctx, library.JobFilter{Status: jobs.StatusSucceeded})
	if err != nil {
		t.Fatalf("ListJobs(succeeded): %v", err)
	}
	if len(succeeded) != 1 || succeeded[0].Task.ID != doneID {
		t.Fatalf("status filter returned %+v, want only the finished job", succeeded)
	}
}

func TestCountsByStatusGroupsTheQueue(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now).ForSource(f.sourceID)); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	claimed, err := f.tasks.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: %v (task %v)", err, claimed)
	}

	counts, err := f.tasks.CountsByStatus(ctx)
	if err != nil {
		t.Fatalf("CountsByStatus: %v", err)
	}
	if counts[jobs.StatusPending] != 2 {
		t.Errorf("pending = %d, want 2", counts[jobs.StatusPending])
	}
	if counts[jobs.StatusRunning] != 1 {
		t.Errorf("running = %d, want 1", counts[jobs.StatusRunning])
	}
}

func TestDeleteOrphansClearsTasksWhoseMediaIsGone(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	keep, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// A second item whose media row then disappears. A task pointing at media that
	// does not exist can only ever fail with "no rows in result set"; that is the
	// litter this clears.
	doomedID, err := f.media.Upsert(ctx, domain.Media{
		SourceID: f.sourceID, ExternalID: "doomed", Status: domain.MediaPending,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert doomed media: %v", err)
	}
	if _, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(doomedID)); err != nil {
		t.Fatalf("Enqueue orphan: %v", err)
	}
	f.orphanMedia(t, doomedID)

	removed, err := f.tasks.DeleteOrphans(ctx)
	if err != nil {
		t.Fatalf("DeleteOrphans: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	remaining, err := f.tasks.ListJobs(ctx, library.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Task.ID != keep {
		t.Fatalf("remaining jobs = %+v, want only task %d", remaining, keep)
	}
}

func TestRequeueRunningRescuesTasksStrandedByARestart(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	if _, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now).ForSource(f.sourceID)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := f.tasks.Claim(ctx, now); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	requeued, err := f.tasks.RequeueRunning(ctx, now)
	if err != nil {
		t.Fatalf("RequeueRunning: %v", err)
	}
	if requeued != 1 {
		t.Errorf("requeued = %d, want 1", requeued)
	}

	// It must be claimable again, otherwise the work is lost forever.
	claimed, err := f.tasks.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after requeue: %v", err)
	}
	if claimed == nil {
		t.Fatal("Claim after requeue returned nil; the stranded task was not recovered")
	}
}

func TestActiveMediaIDsListsOnlyUnfinishedWork(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	pendingTask, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	active, err := f.tasks.ActiveMediaIDs(ctx)
	if err != nil {
		t.Fatalf("ActiveMediaIDs: %v", err)
	}
	if !active[f.mediaID] {
		t.Errorf("media %d missing from %v", f.mediaID, active)
	}

	if err := f.tasks.Complete(ctx, pendingTask, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	active, err = f.tasks.ActiveMediaIDs(ctx)
	if err != nil {
		t.Fatalf("ActiveMediaIDs: %v", err)
	}
	if active[f.mediaID] {
		t.Errorf("finished work should not count as active: %v", active)
	}
}

func TestRetryRequeuesAFailedJobWithAFreshBudget(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	id, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := f.tasks.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: %v", err)
	}
	claimed.Attempts = claimed.MaxAttempts
	if err := f.tasks.Fail(ctx, *claimed, "boom", now); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if err := f.tasks.Retry(ctx, id, now); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	job, err := f.tasks.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Task.Status != jobs.StatusPending {
		t.Errorf("status = %q, want %q", job.Task.Status, jobs.StatusPending)
	}
	if job.Task.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 so the retry gets a full budget", job.Task.Attempts)
	}
	if job.Task.LastError != "" {
		t.Errorf("LastError = %q, want it cleared", job.Task.LastError)
	}
}

func TestRetryRefusesJobsThatAreStillQueued(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	id, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	err = f.tasks.Retry(ctx, id, now)
	if !errors.Is(err, library.ErrJobNotRetryable) {
		t.Fatalf("Retry on a queued job = %v, want ErrJobNotRetryable", err)
	}
}

// finish enqueues a task and drives it to a settled state, returning its id.
func (f queueFixture) finish(t *testing.T, status jobs.TaskStatus, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, at).ForSource(f.sourceID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := f.tasks.Claim(ctx, at)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: %v", err)
	}
	if status == jobs.StatusSucceeded {
		if err := f.tasks.Complete(ctx, claimed.ID, at); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		return id
	}
	claimed.Attempts = claimed.MaxAttempts
	if err := f.tasks.Fail(ctx, *claimed, "boom", at); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	return id
}

func TestDeleteRemovesAFinishedJobButRefusesARunningOne(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	done := f.finish(t, jobs.StatusSucceeded, now)
	if err := f.tasks.Delete(ctx, done); err != nil {
		t.Fatalf("Delete(finished): %v", err)
	}
	if _, err := f.tasks.GetJob(ctx, done); !IsNotFound(err) {
		t.Errorf("job %d still present after delete: %v", done, err)
	}

	// A running task's worker is still going to report an outcome for it.
	running, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now).ForSource(f.sourceID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := f.tasks.Claim(ctx, now); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := f.tasks.Delete(ctx, running); !errors.Is(err, library.ErrJobNotDeletable) {
		t.Fatalf("Delete(running) = %v, want ErrJobNotDeletable", err)
	}
}

func TestDeleteFinishedLeavesQueuedAndRunningWorkAlone(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	f.finish(t, jobs.StatusSucceeded, now)
	f.finish(t, jobs.StatusFailed, now)
	queued, err := f.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(f.mediaID))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	removed, err := f.tasks.DeleteFinished(ctx)
	if err != nil {
		t.Fatalf("DeleteFinished: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	remaining, err := f.tasks.ListJobs(ctx, library.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Task.ID != queued {
		t.Fatalf("remaining = %+v, want only the queued job %d", remaining, queued)
	}
}

func TestDeleteFinishedBeforeKeepsRecentHistory(t *testing.T) {
	f := newQueueFixture(t)
	ctx := context.Background()

	old := now.Add(-30 * 24 * time.Hour)
	oldJob := f.finish(t, jobs.StatusSucceeded, old)
	recentJob := f.finish(t, jobs.StatusSucceeded, now)

	removed, err := f.tasks.DeleteFinishedBefore(ctx, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteFinishedBefore: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	if _, err := f.tasks.GetJob(ctx, oldJob); !IsNotFound(err) {
		t.Errorf("the job outside the window should be gone, got %v", err)
	}
	if _, err := f.tasks.GetJob(ctx, recentJob); err != nil {
		t.Errorf("the job inside the window should remain, got %v", err)
	}
}

func TestGetJobReportsAMissingJobAsNotFound(t *testing.T) {
	f := newQueueFixture(t)

	_, err := f.tasks.GetJob(context.Background(), 999)
	if !IsNotFound(err) {
		t.Fatalf("GetJob(999) = %v, want a not-found error", err)
	}
}
