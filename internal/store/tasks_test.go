package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sub_scribe/internal/jobs"
)

// now is a fixed reference time for deterministic scheduling assertions.
var now = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// newTestDB opens a fresh migrated database in a temp file and registers cleanup.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestClaimReturnsNilWhenEmpty(t *testing.T) {
	repo := newTestDB(t).Tasks()
	claimed, err := repo.Claim(context.Background(), now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed != nil {
		t.Errorf("Claim() = %+v, want nil", claimed)
	}
}

func TestEnqueueAndClaimMarksRunningAndCountsAttempt(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	if _, err := repo.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now)); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed == nil {
		t.Fatal("Claim() = nil, want a task")
	}
	if claimed.Status != jobs.StatusRunning {
		t.Errorf("Status = %q, want running", claimed.Status)
	}
	if claimed.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", claimed.Attempts)
	}
}

func TestClaimHonorsPriorityThenAge(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	low := jobs.NewTask(jobs.TaskDownloadMedia, now)
	high := jobs.NewTask(jobs.TaskIndexSource, now)
	high.Priority = 10
	if _, err := repo.Enqueue(ctx, low); err != nil {
		t.Fatalf("Enqueue(low) error = %v", err)
	}
	if _, err := repo.Enqueue(ctx, high); err != nil {
		t.Fatalf("Enqueue(high) error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.Type != jobs.TaskIndexSource {
		t.Errorf("claimed type = %q, want the higher-priority index_source", claimed.Type)
	}
}

func TestClaimSkipsTasksScheduledInFuture(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	future := jobs.NewTask(jobs.TaskCleanup, now)
	future.RunAfter = now.Add(time.Hour)
	if _, err := repo.Enqueue(ctx, future); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed != nil {
		t.Errorf("Claim() = %+v, want nil for future task", claimed)
	}
}

func TestCompleteRemovesTaskFromPendingCount(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	id, err := repo.Enqueue(ctx, jobs.NewTask(jobs.TaskIndexSource, now))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := repo.Claim(ctx, now); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repo.Complete(ctx, id, now); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	count, err := repo.PendingCount(ctx)
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("PendingCount() = %d, want 0", count)
	}
}

func TestFailRequeuesWithBackoffWhenRetriesRemain(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	if _, err := repo.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now)); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repo.Fail(ctx, *claimed, "network blip", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	// Not runnable immediately (backoff), but runnable in the future.
	if again, _ := repo.Claim(ctx, now); again != nil {
		t.Error("task should not be immediately reclaimable after backoff")
	}
	requeued, err := repo.Claim(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Claim(future) error = %v", err)
	}
	if requeued == nil {
		t.Fatal("expected task to be reclaimable after backoff window")
	}
	if requeued.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 on second claim", requeued.Attempts)
	}
}

func TestFailMarksFailedWhenRetriesExhausted(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	task := jobs.NewTask(jobs.TaskDownloadMedia, now)
	task.MaxAttempts = 1
	if _, err := repo.Enqueue(ctx, task); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repo.Fail(ctx, *claimed, "permanent error", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	count, err := repo.PendingCount(ctx)
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("PendingCount() = %d, want 0 (task should be failed, not pending)", count)
	}
}
