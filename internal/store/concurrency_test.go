package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"sub_scribe/internal/jobs"
)

// TestConcurrentReadsAndWritesDoNotError exercises the connection pool the way the
// running app does: many web-style reads overlapping with background-worker
// writes. With WAL mode, a pool, and busy_timeout, none of these should fail or
// deadlock. This guards the switch away from a single serialized connection.
func TestConcurrentReadsAndWritesDoNotError(t *testing.T) {
	db := newTestDB(t)
	tasks := db.Tasks()
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	const workers = 24
	const opsPerWorker = 20

	var wg sync.WaitGroup
	errs := make(chan error, workers*opsPerWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(readerHalf bool) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				if readerHalf {
					if _, err := tasks.PendingCount(ctx); err != nil {
						errs <- err
					}
					continue
				}
				if _, err := tasks.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now)); err != nil {
					errs <- err
				}
			}
		}(w%2 == 0)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent DB op failed: %v", err)
	}
}
