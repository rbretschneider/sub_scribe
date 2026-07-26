// Package scheduler periodically enqueues background work for sources that are
// due: an index scan to discover new media, and a retention cleanup where
// configured. It is the heartbeat that turns sub_scribe from on-demand into
// automatic, without ever doing slow work itself — it only enqueues tasks.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// defaultInterval is how often the scheduler wakes to look for due sources.
const defaultInterval = 1 * time.Minute

// pruneInterval is how often queue history is trimmed. Retention is measured in
// days, so checking a few times a day is ample and keeps the queue from filling
// with prune tasks.
const pruneInterval = 6 * time.Hour

// SourceScheduler is the narrow slice of source persistence the scheduler needs.
// The store's source repository satisfies it structurally.
type SourceScheduler interface {
	DueForIndex(ctx context.Context, now time.Time) ([]domain.Source, error)
	MarkIndexed(ctx context.Context, id int64, now time.Time) error
}

// Enqueuer schedules a task for later execution. The store's task repository
// satisfies it.
type Enqueuer interface {
	Enqueue(ctx context.Context, task jobs.Task) (int64, error)
}

// Config groups the scheduler's collaborators and tunables so the constructor
// keeps a small signature.
type Config struct {
	Sources  SourceScheduler
	Tasks    Enqueuer
	Clock    jobs.Clock
	Interval time.Duration
	Logger   *slog.Logger
}

// Scheduler enqueues index and cleanup tasks for due sources on a fixed cadence.
type Scheduler struct {
	sources  SourceScheduler
	tasks    Enqueuer
	clock    jobs.Clock
	interval time.Duration
	log      *slog.Logger
	// lastPrune is when queue history was last trimmed. It is deliberately kept in
	// memory rather than persisted: the only cost of forgetting it across a
	// restart is one extra prune, and a prune is idempotent. Only Tick touches it,
	// and Run calls Tick from a single goroutine.
	lastPrune time.Time
}

// New constructs a Scheduler, applying defaults for any unset configuration.
func New(cfg Config) *Scheduler {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Scheduler{
		sources:  cfg.Sources,
		tasks:    cfg.Tasks,
		clock:    cfg.Clock,
		interval: cfg.Interval,
		log:      cfg.Logger,
	}
}

// Run ticks until the context is cancelled, enqueuing work each interval.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				s.log.Error("scheduler tick failed", "error", err)
			}
		}
	}
}

// Tick enqueues an index task for every due source and a cleanup task where
// retention is configured, then marks each source indexed so it is not enqueued
// again until its frequency elapses. Marking at enqueue time (optimistically)
// prevents duplicate index tasks stacking up between ticks; a failed index is
// retried by the task queue's own backoff, not by re-enqueue here.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.clock.Now()
	s.enqueuePruneIfDue(ctx, now)

	due, err := s.sources.DueForIndex(ctx, now)
	if err != nil {
		return err
	}
	for _, source := range due {
		if err := s.enqueueForSource(ctx, source, now); err != nil {
			s.log.Error("enqueue for source failed", "source_id", source.ID, "error", err)
			continue
		}
	}
	return nil
}

// enqueuePruneIfDue schedules a trim of finished queue history when enough time
// has passed. A failure here is logged rather than returned: tidying history must
// never stop sources from being scanned.
func (s *Scheduler) enqueuePruneIfDue(ctx context.Context, now time.Time) {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < pruneInterval {
		return
	}
	s.lastPrune = now
	if _, err := s.tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskPruneJobs, now)); err != nil {
		s.log.Error("enqueue prune jobs failed", "error", err)
	}
}

// enqueueForSource enqueues the index task for one source, plus a cleanup task
// where retention is configured and a redownload scan (a cheap no-op unless the
// source's profile sets a redownload age), then marks the source indexed.
func (s *Scheduler) enqueueForSource(ctx context.Context, source domain.Source, now time.Time) error {
	indexTask := jobs.NewTask(jobs.TaskIndexSource, now).ForSource(source.ID)
	if _, err := s.tasks.Enqueue(ctx, indexTask); err != nil {
		return err
	}
	if source.RetentionAfter > 0 {
		cleanupTask := jobs.NewTask(jobs.TaskCleanup, now).ForSource(source.ID)
		if _, err := s.tasks.Enqueue(ctx, cleanupTask); err != nil {
			return err
		}
	}
	redownloadTask := jobs.NewTask(jobs.TaskRedownload, now).ForSource(source.ID)
	if _, err := s.tasks.Enqueue(ctx, redownloadTask); err != nil {
		return err
	}
	return s.sources.MarkIndexed(ctx, source.ID, now)
}
