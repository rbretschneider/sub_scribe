// Package jobs provides sub_scribe's asynchronous work backbone: a durable task
// queue backed by the database and a pool of workers that drain it. Every slow
// operation (indexing a channel, downloading media, generating feeds) runs as a
// task so user-facing actions return immediately — the core fix for the UI lag
// that grows with channel count in the original design.
package jobs

import "time"

// TaskType identifies a unit of background work. New kinds of work are added by
// introducing a type and registering a handler for it (Open-Closed), never by
// editing the dispatcher.
type TaskType string

const (
	// TaskIndexSource scans a source's remote collection for new media.
	TaskIndexSource TaskType = "index_source"
	// TaskDownloadMedia downloads a single pending media item.
	TaskDownloadMedia TaskType = "download_media"
	// TaskGenerateFeed rebuilds a source's RSS/podcast feed.
	TaskGenerateFeed TaskType = "generate_feed"
	// TaskCleanup enforces a source's retention policy.
	TaskCleanup TaskType = "cleanup"
	// TaskRedownload re-queues a source's media that is due for a quality refresh.
	TaskRedownload TaskType = "redownload"
	// TaskPruneJobs removes finished queue entries past their retention window, so
	// the queue history stays a useful record rather than an unbounded log.
	TaskPruneJobs TaskType = "prune_jobs"
)

// TaskStatus is the lifecycle state of a queued task.
type TaskStatus string

const (
	// StatusPending is queued and eligible to run once RunAfter has passed.
	StatusPending TaskStatus = "pending"
	// StatusRunning has been claimed by a worker.
	StatusRunning TaskStatus = "running"
	// StatusSucceeded completed without error.
	StatusSucceeded TaskStatus = "succeeded"
	// StatusFailed exhausted its retry budget.
	StatusFailed TaskStatus = "failed"
)

// defaultMaxAttempts is the retry budget for a task before it is marked failed.
const defaultMaxAttempts = 3

// Task is one durable unit of background work. SourceID and MediaID are optional
// associations that scope the work and let the UI show progress per source.
type Task struct {
	ID          int64
	Type        TaskType
	Status      TaskStatus
	SourceID    *int64
	MediaID     *int64
	Priority    int
	Attempts    int
	MaxAttempts int
	LastError   string
	RunAfter    time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewTask builds a pending task of the given type with default retry budget,
// eligible to run immediately.
func NewTask(taskType TaskType, now time.Time) Task {
	return Task{
		Type:        taskType,
		Status:      StatusPending,
		MaxAttempts: defaultMaxAttempts,
		RunAfter:    now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ForSource associates the task with a source and returns it, enabling fluent
// construction at call sites.
func (t Task) ForSource(sourceID int64) Task {
	t.SourceID = &sourceID
	return t
}

// ForMedia associates the task with a media item and returns it.
func (t Task) ForMedia(mediaID int64) Task {
	t.MediaID = &mediaID
	return t
}

// CanRetry reports whether a failed attempt should be requeued rather than marked
// permanently failed.
func (t Task) CanRetry() bool {
	return t.Attempts < t.MaxAttempts
}

// NextRunAfter returns when a retryable task should next become eligible, applying
// exponential backoff based on how many attempts have already been made.
func (t Task) NextRunAfter(now time.Time) time.Time {
	return now.Add(backoffFor(t.Attempts))
}

// backoffFor returns the delay before a task's next attempt, growing with the
// attempt count so transient provider errors are retried with increasing spacing.
func backoffFor(attempts int) time.Duration {
	const base = 30 * time.Second
	delay := base
	for i := 1; i < attempts; i++ {
		delay *= 2
	}
	const max = 15 * time.Minute
	if delay > max {
		return max
	}
	return delay
}
