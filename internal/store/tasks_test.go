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

func TestFailMarksTaskStatusAndRecordsError(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	task := jobs.NewTask(jobs.TaskDownloadMedia, now)
	task.MaxAttempts = 1 // exhaust budget on first claim so Fail marks it failed
	id, err := repo.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != id {
		t.Errorf("Claim() = %d, want %d", claimed.ID, id)
	}
	if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
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

func TestRetryAllFailedRequeuesFailedTasks(t *testing.T) {
	db := newTestDB(t)
	repo := db.Tasks()
	ctx := context.Background()

	profileID := seedProfile(t, db)
	sourceID := seedSource(t, db, profileID.ID, true, nil)

	// Enqueue four tasks for the source so we can fail three of them.
	// MaxAttempts=1 ensures Fail marks them failed (not requeued) after one claim.
	taskIDs := make([]int64, 0, 4)
	for i := 0; i < 4; i++ {
		task := jobs.NewTask(jobs.TaskDownloadMedia, now)
		task.SourceID = &sourceID.ID
		task.MaxAttempts = 1
		id, err := repo.Enqueue(ctx, task)
		if err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
		taskIDs = append(taskIDs, id)
	}

	// Claim and fail the first three.
	for _, id := range taskIDs[:3] {
		claimed, err := repo.Claim(ctx, now)
		if err != nil {
			t.Fatalf("Claim() error = %v", err)
		}
		if claimed.ID != id {
			t.Errorf("Claim() = %d, want %d", claimed.ID, id)
		}
		if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
	}

	// One task should still be pending (the one we never claimed).
	pending, err := repo.PendingCount(ctx)
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if pending != 1 {
		t.Errorf("PendingCount() = %d, want 1", pending)
	}

	// Retry all failed within a generous cutoff.
	cutoff := now.Add(-24 * time.Hour)
	n, err := repo.RetryAllFailed(ctx, sourceID.ID, cutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 3 {
		t.Errorf("RetryAllFailed() = %d, want 3", n)
	}

	pending, err = repo.PendingCount(ctx)
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if pending != 4 {
		t.Errorf("PendingCount() = %d, want 4 (3 retried + 1 untouched)", pending)
	}
}

func TestRetryAllFailedOnlyTouchesFailedTasks(t *testing.T) {
	db := newTestDB(t)
	repo := db.Tasks()
	ctx := context.Background()

	profileID := seedProfile(t, db)
	sourceID := seedSource(t, db, profileID.ID, true, nil)

	// One failed task, one still pending, one running.
	// MaxAttempts=1 ensures Fail marks it failed (not requeued) after one claim.
	failedTask := jobs.NewTask(jobs.TaskDownloadMedia, now)
	failedTask.SourceID = &sourceID.ID
	failedTask.MaxAttempts = 1
	failedID, err := repo.Enqueue(ctx, failedTask)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	pendingTask := jobs.NewTask(jobs.TaskDownloadMedia, now)
	pendingTask.SourceID = &sourceID.ID
	pendingID, err := repo.Enqueue(ctx, pendingTask)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	runningTask := jobs.NewTask(jobs.TaskDownloadMedia, now)
	runningTask.SourceID = &sourceID.ID
	runningID, err := repo.Enqueue(ctx, runningTask)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// Fail the first one.
	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != failedID {
		t.Errorf("Claim() = %d, want %d", claimed.ID, failedID)
	}
	if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	// Claim and keep the second running.
	claimed, err = repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != pendingID {
		t.Errorf("Claim() = %d, want %d", claimed.ID, pendingID)
	}

	cutoff := now.Add(-24 * time.Hour)
	n, err := repo.RetryAllFailed(ctx, sourceID.ID, cutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 1 {
		t.Errorf("RetryAllFailed() = %d, want 1", n)
	}

	// The failed task should now be pending.
	claimed, err = repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != failedID {
		t.Errorf("Claim() = %d, want %d (failed task should now be pending)", claimed.ID, failedID)
	}
	if claimed.Status != jobs.StatusRunning {
		t.Errorf("Status = %q, want running", claimed.Status)
	}

	// The running task should still be running.
	claimed, err = repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != runningID {
		t.Errorf("Claim() = %d, want %d (running task should still be running)", claimed.ID, runningID)
	}
}

func TestRetryAllFailedRespectsCutoff(t *testing.T) {
	db := newTestDB(t)
	repo := db.Tasks()
	ctx := context.Background()

	profileID := seedProfile(t, db)
	sourceID := seedSource(t, db, profileID.ID, true, nil)

	// Enqueue a task, fail it, and push its updated_at back before the cutoff.
	task := jobs.NewTask(jobs.TaskDownloadMedia, now)
	task.SourceID = &sourceID.ID
	task.MaxAttempts = 1
	id, err := repo.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != id {
		t.Errorf("Claim() = %d, want %d", claimed.ID, id)
	}
	if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	// Nudge updated_at into the past — older than the cutoff.
	oldTime := now.Add(-48 * time.Hour)
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE tasks SET updated_at = ? WHERE id = ?`,
		oldTime.Unix(), id); err != nil {
		t.Fatalf("UPDATE tasks error = %v", err)
	}

	// Cutoff is one day ago — the task is outside it.
	cutoff := now.Add(-24 * time.Hour)
	n, err := repo.RetryAllFailed(ctx, sourceID.ID, cutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 0 {
		t.Errorf("RetryAllFailed() = %d, want 0 (task is before cutoff)", n)
	}

	// A cutoff far enough back should requeue it.
	oldCutoff := now.Add(-366 * 24 * time.Hour)
	n, err = repo.RetryAllFailed(ctx, sourceID.ID, oldCutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 1 {
		t.Errorf("RetryAllFailed() = %d, want 1 (task is inside wide cutoff)", n)
	}
}

func TestRetryAllFailedReturnsZeroWhenNoneMatch(t *testing.T) {
	db := newTestDB(t)
	repo := db.Tasks()
	ctx := context.Background()

	profileID := seedProfile(t, db)
	sourceID := seedSource(t, db, profileID.ID, true, nil)

	// No tasks for the source — nothing to do.
	cutoff := now.Add(-24 * time.Hour)
	n, err := repo.RetryAllFailed(ctx, sourceID.ID, cutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 0 {
		t.Errorf("RetryAllFailed() = %d, want 0", n)
	}
}

func TestRetryAllFailedDoesNotTouchOtherSources(t *testing.T) {
	db := newTestDB(t)
	repo := db.Tasks()
	ctx := context.Background()

	profileID := seedProfile(t, db)
	sourceA := seedSource(t, db, profileID.ID, true, nil)
	sourceB := seedSource(t, db, profileID.ID, true, nil)

	// One failed task for each source.
	taskA := jobs.NewTask(jobs.TaskDownloadMedia, now)
	taskA.SourceID = &sourceA.ID
	taskA.MaxAttempts = 1
	idA, err := repo.Enqueue(ctx, taskA)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	taskB := jobs.NewTask(jobs.TaskDownloadMedia, now)
	taskB.SourceID = &sourceB.ID
	taskB.MaxAttempts = 1
	idB, err := repo.Enqueue(ctx, taskB)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	claimed, err := repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != idA {
		t.Errorf("Claim() = %d, want %d", claimed.ID, idA)
	}
	if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	claimed, err = repo.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != idB {
		t.Errorf("Claim() = %d, want %d", claimed.ID, idB)
	}
	if err := repo.Fail(ctx, *claimed, "boom", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	// Retry failed for source A only — source B should stay failed.
	cutoff := now.Add(-24 * time.Hour)
	n, err := repo.RetryAllFailed(ctx, sourceA.ID, cutoff, now)
	if err != nil {
		t.Fatalf("RetryAllFailed() error = %v", err)
	}
	if n != 1 {
		t.Errorf("RetryAllFailed() = %d, want 1", n)
	}

	// Source B's task should still be failed — verify directly via query.
	var status jobs.TaskStatus
	row := db.sql.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, idB)
	if err := row.Scan(&status); err != nil {
		t.Fatalf("scan source B task status: %v", err)
	}
	if status != jobs.StatusFailed {
		t.Errorf("source B task status = %q, want %q", status, jobs.StatusFailed)
	}
}
