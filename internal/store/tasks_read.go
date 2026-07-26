package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// TaskRepo also serves the queue's read and maintenance views.
var (
	_ library.JobReader     = (*TaskRepo)(nil)
	_ library.QueueMaintain = (*TaskRepo)(nil)
)

// jobColumns selects a task together with the names of whatever it acts on. A
// download task carries only a media id, so its source is reached through the
// media row — coalescing the two keeps every job showing a channel in the UI.
const jobColumns = `t.id, t.type, t.status, t.source_id, t.media_id, t.priority,
	t.attempts, t.max_attempts, t.last_error, t.run_after, t.created_at, t.updated_at,
	COALESCE(t.source_id, m.source_id), COALESCE(s.name, ms.name, ''),
	COALESCE(m.title, ''), COALESCE(m.external_id, ''), COALESCE(m.status, '')`

// jobJoins resolves a task's source directly and through its media item.
const jobJoins = `FROM tasks t
	LEFT JOIN sources s ON s.id = t.source_id
	LEFT JOIN media m ON m.id = t.media_id
	LEFT JOIN sources ms ON ms.id = m.source_id`

// jobOrder shows the most recent activity first. It is a bare column order so
// idx_tasks_updated can serve it and the query stops at the limit; ranking
// running and queued work above finished work is done by querying the two groups
// separately rather than with a CASE, which no index can satisfy.
const jobOrder = `ORDER BY t.updated_at DESC, t.id DESC`

// ListJobs returns queue entries matching filter: work in flight first, then
// finished work, each newest-first.
//
// An unfiltered listing runs as two indexed queries rather than one sorted over
// the whole table. The queue accumulates an entry per video indexed, so on a
// large archive that difference is the whole cost of the page.
func (r *TaskRepo) ListJobs(ctx context.Context, filter library.JobFilter) ([]library.JobListItem, error) {
	if filter.Status != "" {
		return r.listJobsWhere(ctx, `WHERE t.status = ?`, filter.Limit, filter.Status)
	}

	active, err := r.listJobsWhere(ctx, `WHERE t.status IN (?, ?)`, filter.Limit,
		jobs.StatusRunning, jobs.StatusPending)
	if err != nil {
		return nil, err
	}
	if filter.Limit > 0 && len(active) >= filter.Limit {
		return active, nil
	}

	remaining := 0
	if filter.Limit > 0 {
		remaining = filter.Limit - len(active)
	}
	finished, err := r.listJobsWhere(ctx, `WHERE t.status NOT IN (?, ?)`, remaining,
		jobs.StatusRunning, jobs.StatusPending)
	if err != nil {
		return nil, err
	}
	return append(active, finished...), nil
}

// listJobsWhere runs one indexed page of the job listing.
func (r *TaskRepo) listJobsWhere(ctx context.Context, where string, limit int, args ...any) ([]library.JobListItem, error) {
	query := `SELECT ` + jobColumns + ` ` + jobJoins + ` ` + where + ` ` + jobOrder
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()

	var items []library.JobListItem
	for rows.Next() {
		item, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan job: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetJob returns a single queue entry with its target names resolved.
func (r *TaskRepo) GetJob(ctx context.Context, id int64) (library.JobListItem, error) {
	row := r.sql.QueryRowContext(ctx, `SELECT `+jobColumns+` `+jobJoins+` WHERE t.id = ?`, id)
	item, err := scanJob(row)
	if err != nil {
		return library.JobListItem{}, fmt.Errorf("store: get job %d: %w", id, err)
	}
	return item, nil
}

// CountsByStatus returns the number of queue entries in each status, for the
// filter chips on the jobs screen.
func (r *TaskRepo) CountsByStatus(ctx context.Context) (map[jobs.TaskStatus]int, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("store: job counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[jobs.TaskStatus]int)
	for rows.Next() {
		var status jobs.TaskStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("store: scan job count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// Retry puts a finished task back in the queue for an immediate fresh attempt,
// clearing the error and the attempt count so the user gets the full retry
// budget rather than one last try.
func (r *TaskRepo) Retry(ctx context.Context, id int64, now time.Time) error {
	res, err := r.sql.ExecContext(ctx,
		`UPDATE tasks SET status = ?, attempts = 0, last_error = '', run_after = ?, updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		jobs.StatusPending, now.Unix(), now.Unix(), id, jobs.StatusFailed, jobs.StatusSucceeded)
	if err != nil {
		return fmt.Errorf("store: retry task %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: retry task %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("store: retry task %d: %w", id, library.ErrJobNotRetryable)
	}
	return nil
}

// Delete removes a single queue entry. A running task is refused: its worker is
// still going to report an outcome, and deleting the row out from under it would
// turn a completed download into a confusing "recording task success failed"
// error in the log.
func (r *TaskRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.sql.ExecContext(ctx,
		`DELETE FROM tasks WHERE id = ? AND status != ?`, id, jobs.StatusRunning)
	if err != nil {
		return fmt.Errorf("store: delete task %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete task %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("store: delete task %d: %w", id, library.ErrJobNotDeletable)
	}
	return nil
}

// DeleteFinished removes every settled queue entry, for the "clear finished"
// action. Queued and running work is left alone. It returns how many were
// removed.
func (r *TaskRepo) DeleteFinished(ctx context.Context) (int, error) {
	return r.deleteFinished(ctx, `DELETE FROM tasks WHERE status IN (?, ?)`,
		jobs.StatusSucceeded, jobs.StatusFailed)
}

// DeleteFinishedBefore removes settled queue entries last touched before cutoff,
// which is how the retention window is enforced. It returns how many were
// removed.
func (r *TaskRepo) DeleteFinishedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	return r.deleteFinished(ctx,
		`DELETE FROM tasks WHERE status IN (?, ?) AND updated_at < ?`,
		jobs.StatusSucceeded, jobs.StatusFailed, cutoff.Unix())
}

// deleteFinished runs a delete of settled tasks and reports the row count, so
// the two retention entry points share one implementation.
func (r *TaskRepo) deleteFinished(ctx context.Context, query string, args ...any) (int, error) {
	res, err := r.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("store: delete finished tasks: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete finished tasks: %w", err)
	}
	return int(affected), nil
}

// DeleteOrphans removes tasks whose media item no longer exists. Such rows can
// only ever fail with "no rows in result set", so they are queue litter that
// hides real failures; it returns how many were cleared.
func (r *TaskRepo) DeleteOrphans(ctx context.Context) (int, error) {
	res, err := r.sql.ExecContext(ctx,
		`DELETE FROM tasks
		 WHERE media_id IS NOT NULL
		   AND media_id NOT IN (SELECT id FROM media)`)
	if err != nil {
		return 0, fmt.Errorf("store: delete orphan tasks: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete orphan tasks: %w", err)
	}
	return int(affected), nil
}

// RequeueRunning returns tasks left mid-flight by a restart to the pending
// queue. A claimed task is otherwise stranded forever, because only Claim moves
// a task out of running. It returns how many were requeued.
func (r *TaskRepo) RequeueRunning(ctx context.Context, now time.Time) (int, error) {
	res, err := r.sql.ExecContext(ctx,
		`UPDATE tasks SET status = ?, run_after = ?, updated_at = ? WHERE status = ?`,
		jobs.StatusPending, now.Unix(), now.Unix(), jobs.StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("store: requeue running tasks: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: requeue running tasks: %w", err)
	}
	return int(affected), nil
}

// ActiveMediaIDs returns the media ids that already have an unfinished download
// task, so the caller can requeue only what is genuinely stranded.
func (r *TaskRepo) ActiveMediaIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT DISTINCT media_id FROM tasks
		 WHERE media_id IS NOT NULL AND status IN (?, ?)`,
		jobs.StatusPending, jobs.StatusRunning)
	if err != nil {
		return nil, fmt.Errorf("store: active media ids: %w", err)
	}
	defer rows.Close()

	active := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan active media id: %w", err)
		}
		active[id] = true
	}
	return active, rows.Err()
}

// scanJob maps a joined task row into a JobListItem.
func scanJob(row rowScanner) (library.JobListItem, error) {
	var (
		item                         library.JobListItem
		sourceID, mediaID, targetSrc sql.NullInt64
		runAfter, createdAt, updated int64
	)
	if err := row.Scan(
		&item.Task.ID, &item.Task.Type, &item.Task.Status, &sourceID, &mediaID,
		&item.Task.Priority, &item.Task.Attempts, &item.Task.MaxAttempts,
		&item.Task.LastError, &runAfter, &createdAt, &updated,
		&targetSrc, &item.SourceName, &item.MediaTitle, &item.MediaExternalID,
		&item.MediaStatus,
	); err != nil {
		return library.JobListItem{}, err
	}
	item.Task.SourceID = fromNullID(sourceID)
	item.Task.MediaID = fromNullID(mediaID)
	item.Task.RunAfter = fromUnix(runAfter)
	item.Task.CreatedAt = fromUnix(createdAt)
	item.Task.UpdatedAt = fromUnix(updated)
	item.SourceID = fromNullID(targetSrc)
	item.MediaID = item.Task.MediaID
	return item, nil
}

// IsNotFound reports whether err came from asking for a row that is not there,
// letting the web layer answer 404 without importing database/sql.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
