package library

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// Reconcile repairs queue and media state left inconsistent by a restart or by
// storage that lost writes, and reports what it fixed.
//
// Without this the app can wedge in states it will never leave on its own: a
// task claimed by a worker that died stays "running" forever, media stuck in
// "downloading" is never retried, and a media row that vanished leaves tasks
// that can only ever fail with "no rows in result set" — which is exactly the
// noise that buries real errors. Recovery runs at startup, before any worker
// claims work.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var report ReconcileReport

	removed, err := s.deps.Queue.DeleteOrphans(ctx)
	if err != nil {
		return report, fmt.Errorf("remove orphan tasks: %w", err)
	}
	report.OrphanTasksRemoved = removed

	now := s.deps.Clock.Now()
	requeued, err := s.deps.Queue.RequeueRunning(ctx, now)
	if err != nil {
		return report, fmt.Errorf("requeue running tasks: %w", err)
	}
	report.RunningTasksRequeued = requeued

	interrupted, err := s.resetInterruptedDownloads(ctx)
	if err != nil {
		return report, err
	}
	report.InterruptedDownloads = interrupted

	// Adopt before requeuing, so an item whose file is already on disk is recorded
	// rather than queued for a download that would just fetch it again.
	adopted, err := s.adoptExistingFiles(ctx)
	if err != nil {
		return report, err
	}
	report.ExistingFilesAdopted = adopted

	moved, err := s.repairMovedFiles(ctx)
	if err != nil {
		return report, err
	}
	report.MovedFilesRepaired = moved

	stranded, err := s.requeueStrandedMedia(ctx)
	if err != nil {
		return report, err
	}
	report.StrandedMediaQueued = stranded

	s.logReconcile(ctx, report)
	return report, nil
}

// PruneJobs removes finished queue entries older than the configured retention
// window, reporting how many were removed. A zero window keeps history forever,
// which is a deliberate choice rather than an accident: indexing a large channel
// creates hundreds of entries, so unbounded history is a real cost, but some
// users would rather pay it than lose the record.
func (s *Service) PruneJobs(ctx context.Context) (int, error) {
	if s.deps.JobRetention <= 0 {
		return 0, nil
	}

	cutoff := s.deps.Clock.Now().Add(-s.deps.JobRetention)
	removed, err := s.deps.Queue.DeleteFinishedBefore(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune finished jobs: %w", err)
	}
	if removed > 0 {
		slog.InfoContext(ctx, "pruned finished jobs past their retention window",
			"removed", removed, "older_than", cutoff.Format(time.RFC3339))
	}
	return removed, nil
}

// resetInterruptedDownloads returns media that was mid-download when the process
// stopped to the pending state, so the requeue pass below picks it up.
func (s *Service) resetInterruptedDownloads(ctx context.Context) (int, error) {
	downloading, err := s.deps.Media.ListByStatus(ctx, domain.MediaDownloading, 0)
	if err != nil {
		return 0, fmt.Errorf("list interrupted downloads: %w", err)
	}
	now := s.deps.Clock.Now()
	for _, media := range downloading {
		if err := s.deps.Media.SetStatus(ctx, media.ID, domain.MediaPending, now); err != nil {
			return 0, fmt.Errorf("reset interrupted download %d: %w", media.ID, err)
		}
	}
	return len(downloading), nil
}

// requeueStrandedMedia enqueues a download for every pending item that has no
// unfinished task. This is what makes a lost queue self-heal: the media rows are
// the record of intent, and the queue is rebuilt to match them.
func (s *Service) requeueStrandedMedia(ctx context.Context) (int, error) {
	pending, err := s.deps.Media.ListByStatus(ctx, domain.MediaPending, 0)
	if err != nil {
		return 0, fmt.Errorf("list pending media: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	active, err := s.deps.Queue.ActiveMediaIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list active queue entries: %w", err)
	}

	now := s.deps.Clock.Now()
	queued := 0
	for _, media := range pending {
		if active[media.ID] {
			continue
		}
		task := jobs.NewTask(jobs.TaskDownloadMedia, now).ForSource(media.SourceID).ForMedia(media.ID)
		if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
			return queued, fmt.Errorf("requeue stranded media %d: %w", media.ID, err)
		}
		queued++
	}
	return queued, nil
}

// logReconcile records what recovery did. A clean start says so once at debug
// level; anything repaired is worth a warning, because it means work was lost.
func (s *Service) logReconcile(ctx context.Context, report ReconcileReport) {
	if report.IsEmpty() {
		slog.DebugContext(ctx, "startup reconcile: queue consistent")
		return
	}
	slog.WarnContext(ctx, "startup reconcile repaired inconsistent state",
		"orphan_tasks_removed", report.OrphanTasksRemoved,
		"running_tasks_requeued", report.RunningTasksRequeued,
		"interrupted_downloads_reset", report.InterruptedDownloads,
		"existing_files_adopted", report.ExistingFilesAdopted,
		"stranded_media_queued", report.StrandedMediaQueued,
		"moved_files_repaired", report.MovedFilesRepaired)
}
