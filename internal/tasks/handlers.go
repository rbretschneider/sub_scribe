// Package tasks bridges the durable job queue to the library service. Each
// exported constructor returns a jobs.HandlerFunc that validates a claimed task
// carries the identifier its library call needs, then delegates to the service.
// Keeping the bridge here lets jobs stay ignorant of the library and the library
// stay ignorant of the queue.
package tasks

import (
	"context"
	"fmt"

	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// Deps holds the library collaborators the task handlers delegate to. It depends
// on the segregated action interfaces, not the concrete service, so the queue is
// decoupled from the library implementation.
type Deps struct {
	Indexer      library.Indexer
	Downloader   library.Downloader
	Retainer     library.Retainer
	Redownloader library.Redownloader
	JobPruner    library.JobPruner
}

// IndexHandler returns a handler that indexes the task's source. The task must
// carry a SourceID; a task without one is a bug in whoever enqueued it, so the
// handler fails loudly rather than silently indexing nothing.
func IndexHandler(indexer library.Indexer) jobs.HandlerFunc {
	return func(ctx context.Context, task jobs.Task) error {
		if task.SourceID == nil {
			return fmt.Errorf("index task %d: missing source id", task.ID)
		}
		return indexer.IndexSource(ctx, *task.SourceID)
	}
}

// DownloadHandler returns a handler that downloads the task's media item. The
// task must carry a MediaID.
func DownloadHandler(downloader library.Downloader) jobs.HandlerFunc {
	return func(ctx context.Context, task jobs.Task) error {
		if task.MediaID == nil {
			return fmt.Errorf("download task %d: missing media id", task.ID)
		}
		return downloader.DownloadMedia(ctx, *task.MediaID)
	}
}

// CleanupHandler returns a handler that enforces the task's source retention
// policy. The task must carry a SourceID.
func CleanupHandler(retainer library.Retainer) jobs.HandlerFunc {
	return func(ctx context.Context, task jobs.Task) error {
		if task.SourceID == nil {
			return fmt.Errorf("cleanup task %d: missing source id", task.ID)
		}
		return retainer.EnforceRetention(ctx, *task.SourceID)
	}
}

// RedownloadHandler returns a handler that re-queues the task's source media due
// for a quality refresh. The task must carry a SourceID.
func RedownloadHandler(redownloader library.Redownloader) jobs.HandlerFunc {
	return func(ctx context.Context, task jobs.Task) error {
		if task.SourceID == nil {
			return fmt.Errorf("redownload task %d: missing source id", task.ID)
		}
		return redownloader.EnforceRedownload(ctx, *task.SourceID)
	}
}

// PruneJobsHandler returns a handler that drops finished queue entries past
// their retention window. It is the one task that acts on the queue itself, so
// it needs neither a source nor a media id.
func PruneJobsHandler(pruner library.JobPruner) jobs.HandlerFunc {
	return func(ctx context.Context, _ jobs.Task) error {
		_, err := pruner.PruneJobs(ctx)
		return err
	}
}
