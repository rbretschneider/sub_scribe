package library

import (
	"context"
	"errors"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// ErrJobNotRetryable reports that a job cannot be retried because it is not in a
// finished state — it is already queued or running.
var ErrJobNotRetryable = errors.New("library: job is not in a retryable state")

// ErrJobNotDeletable reports that a job cannot be removed because it is running,
// or because it no longer exists.
var ErrJobNotDeletable = errors.New("library: job cannot be deleted while it is running")

// JobListItem is one queue entry with its target resolved to names the user
// recognises. The queue stores ids; the screens show "download · <video> ·
// <channel>", so the read model carries both.
type JobListItem struct {
	Task jobs.Task

	// SourceID is the channel this job ultimately belongs to, resolved through the
	// media item for download jobs, which carry no source id of their own.
	SourceID   *int64
	SourceName string

	MediaID         *int64
	MediaTitle      string
	MediaExternalID string
	// MediaStatus is what actually became of the video. A download job that
	// deliberately skipped a video still finishes successfully, so the queue
	// status alone cannot tell "downloaded it" from "decided not to" — this is
	// what makes that difference visible.
	MediaStatus domain.MediaStatus
}

// JobFilter narrows a queue listing. The zero value returns every job, newest
// activity first.
type JobFilter struct {
	// Status limits the listing to one queue status; empty means all statuses.
	Status jobs.TaskStatus
	// Limit caps the number of rows; zero or less means no cap.
	Limit int
}

// JobReader serves the queue screens: what is running, what is waiting, and what
// failed. Read-only by design — the web layer never mutates the queue through it.
type JobReader interface {
	// ListJobs returns queue entries matching filter.
	ListJobs(ctx context.Context, filter JobFilter) ([]JobListItem, error)
	// GetJob returns one queue entry.
	GetJob(ctx context.Context, id int64) (JobListItem, error)
	// CountsByStatus returns how many jobs sit in each status.
	CountsByStatus(ctx context.Context) (map[jobs.TaskStatus]int, error)
}

// QueueMaintain covers the writes that keep the queue tidy: the user's retry and
// delete actions, and the repairs startup recovery makes to state that a crash or
// a lost write left inconsistent. It is separated from JobReader so the screens
// that only display the queue cannot change it.
type QueueMaintain interface {
	// Retry requeues a finished job for a fresh attempt.
	Retry(ctx context.Context, id int64, now time.Time) error
	// RetryAllFailed requeues every failed task for a source whose updated_at
	// falls within cutoff. Returns the number of rows updated.
	RetryAllFailed(ctx context.Context, sourceID int64, cutoff, now time.Time) (int, error)
	// Delete removes one queue entry, refusing to remove a running one.
	Delete(ctx context.Context, id int64) error
	// DeleteFinished removes every settled entry, returning how many were removed.
	DeleteFinished(ctx context.Context) (int, error)
	// DeleteFinishedBefore removes settled entries last touched before cutoff,
	// returning how many were removed.
	DeleteFinishedBefore(ctx context.Context, cutoff time.Time) (int, error)
	// DeleteOrphans removes tasks whose media item no longer exists, returning the
	// number cleared.
	DeleteOrphans(ctx context.Context) (int, error)
	// RequeueRunning returns tasks stranded in the running state to the queue,
	// returning the number requeued.
	RequeueRunning(ctx context.Context, now time.Time) (int, error)
	// ActiveMediaIDs returns media ids that already have an unfinished task.
	ActiveMediaIDs(ctx context.Context) (map[int64]bool, error)
}

// ReconcileReport summarises what startup recovery repaired, so the result is
// visible in the log viewer instead of happening silently.
type ReconcileReport struct {
	OrphanTasksRemoved   int
	RunningTasksRequeued int
	InterruptedDownloads int
	StrandedMediaQueued  int
	// ExistingFilesAdopted counts media whose file was found on disk but was not
	// recorded as downloaded.
	ExistingFilesAdopted int
	// MovedFilesRepaired counts items whose recorded path no longer pointed at the
	// file and was corrected to where the file actually is.
	MovedFilesRepaired int
}

// IsEmpty reports whether recovery found nothing to fix, which is the normal
// case and not worth logging loudly.
func (r ReconcileReport) IsEmpty() bool {
	return r.OrphanTasksRemoved == 0 && r.RunningTasksRequeued == 0 &&
		r.InterruptedDownloads == 0 && r.StrandedMediaQueued == 0 &&
		r.ExistingFilesAdopted == 0 && r.MovedFilesRepaired == 0
}
