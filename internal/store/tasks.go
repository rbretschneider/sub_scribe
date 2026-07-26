package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sub_scribe/internal/jobs"
)

// TaskRepo persists and claims background tasks. It implements the queue contract
// the jobs package depends on, keeping the worker pool ignorant of SQLite.
type TaskRepo struct {
	sql *sql.DB
}

// taskColumns is the shared SELECT/RETURNING column list, kept in one place so
// the scan order and the queries can never drift apart.
const taskColumns = `id, type, status, source_id, media_id, priority,
	attempts, max_attempts, last_error, run_after, created_at, updated_at`

// Enqueue inserts a new task and returns its assigned id.
func (r *TaskRepo) Enqueue(ctx context.Context, task jobs.Task) (int64, error) {
	res, err := r.sql.ExecContext(ctx,
		`INSERT INTO tasks(type, status, source_id, media_id, priority,
			attempts, max_attempts, last_error, run_after, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Type, task.Status, toNullID(task.SourceID), toNullID(task.MediaID),
		task.Priority, task.Attempts, task.MaxAttempts, task.LastError,
		task.RunAfter.Unix(), task.CreatedAt.Unix(), task.UpdatedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue task id: %w", err)
	}
	return id, nil
}

// Claim atomically selects the highest-priority runnable task, marks it running,
// counts the attempt, and returns it. It returns (nil, nil) when nothing is
// runnable, so a caller can simply poll. The single-statement UPDATE...RETURNING
// makes the claim safe even if multiple workers race.
func (r *TaskRepo) Claim(ctx context.Context, now time.Time) (*jobs.Task, error) {
	row := r.sql.QueryRowContext(ctx,
		`UPDATE tasks
		 SET status = ?, attempts = attempts + 1, updated_at = ?
		 WHERE id = (
			SELECT id FROM tasks
			WHERE status = ? AND run_after <= ?
			ORDER BY priority DESC, id ASC
			LIMIT 1
		 )
		 RETURNING `+taskColumns,
		jobs.StatusRunning, now.Unix(), jobs.StatusPending, now.Unix(),
	)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim task: %w", err)
	}
	return &task, nil
}

// Complete marks a claimed task as succeeded.
func (r *TaskRepo) Complete(ctx context.Context, id int64, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE tasks SET status = ?, last_error = '', updated_at = ? WHERE id = ?`,
		jobs.StatusSucceeded, now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: complete task %d: %w", id, err)
	}
	return nil
}

// Fail records a failed attempt. If the task still has retry budget it is
// requeued as pending with a backoff delay; otherwise it is marked failed. The
// task passed in must be the one returned by Claim, so its incremented attempt
// count drives the decision.
func (r *TaskRepo) Fail(ctx context.Context, task jobs.Task, cause string, now time.Time) error {
	if task.CanRetry() {
		return r.requeue(ctx, task, cause, now)
	}
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE tasks SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		jobs.StatusFailed, cause, now.Unix(), task.ID,
	); err != nil {
		return fmt.Errorf("store: fail task %d: %w", task.ID, err)
	}
	return nil
}

// requeue reschedules a retryable task for a later attempt with backoff.
func (r *TaskRepo) requeue(ctx context.Context, task jobs.Task, cause string, now time.Time) error {
	runAfter := task.NextRunAfter(now)
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE tasks SET status = ?, last_error = ?, run_after = ?, updated_at = ? WHERE id = ?`,
		jobs.StatusPending, cause, runAfter.Unix(), now.Unix(), task.ID,
	); err != nil {
		return fmt.Errorf("store: requeue task %d: %w", task.ID, err)
	}
	return nil
}

// PendingCount returns the number of tasks not yet in a terminal state, for the
// dashboard's activity indicator.
func (r *TaskRepo) PendingCount(ctx context.Context) (int, error) {
	var count int
	err := r.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE status IN (?, ?)`,
		jobs.StatusPending, jobs.StatusRunning,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: pending count: %w", err)
	}
	return count, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanTask serves both single-row
// and iterating callers.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask maps a task row into a domain Task, converting stored integers back
// into times and optional ids.
func scanTask(row rowScanner) (jobs.Task, error) {
	var (
		task                         jobs.Task
		sourceID, mediaID            sql.NullInt64
		runAfter, createdAt, updated int64
	)
	if err := row.Scan(
		&task.ID, &task.Type, &task.Status, &sourceID, &mediaID, &task.Priority,
		&task.Attempts, &task.MaxAttempts, &task.LastError, &runAfter, &createdAt, &updated,
	); err != nil {
		return jobs.Task{}, err
	}
	task.SourceID = fromNullID(sourceID)
	task.MediaID = fromNullID(mediaID)
	task.RunAfter = fromUnix(runAfter)
	task.CreatedAt = fromUnix(createdAt)
	task.UpdatedAt = fromUnix(updated)
	return task, nil
}
