package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// fixedClock is a deterministic Clock for tests.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// fakeSources records marked-indexed ids and returns a fixed due list.
type fakeSources struct {
	due       []domain.Source
	markedIDs []int64
}

func (f *fakeSources) DueForIndex(context.Context, time.Time) ([]domain.Source, error) {
	return f.due, nil
}

func (f *fakeSources) MarkIndexed(_ context.Context, id int64, _ time.Time) error {
	f.markedIDs = append(f.markedIDs, id)
	return nil
}

// fakeEnqueuer records every enqueued task's type and source.
type fakeEnqueuer struct {
	tasks []jobs.Task
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, task jobs.Task) (int64, error) {
	f.tasks = append(f.tasks, task)
	return int64(len(f.tasks)), nil
}

func silentScheduler(sources SourceScheduler, tasks Enqueuer) *Scheduler {
	return New(Config{
		Sources: sources,
		Tasks:   tasks,
		Clock:   fixedClock{t: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestTickEnqueuesIndexAndMarksEachDueSource(t *testing.T) {
	sources := &fakeSources{due: []domain.Source{{ID: 1}, {ID: 2}}}
	enqueuer := &fakeEnqueuer{}
	sched := silentScheduler(sources, enqueuer)

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var indexTasks int
	for _, task := range enqueuer.tasks {
		if task.Type == jobs.TaskIndexSource {
			indexTasks++
		}
	}
	if indexTasks != 2 {
		t.Errorf("enqueued %d index tasks, want 2", indexTasks)
	}
	if len(sources.markedIDs) != 2 {
		t.Errorf("marked %d sources indexed, want 2 (prevents duplicate enqueues)", len(sources.markedIDs))
	}
}

func TestTickEnqueuesCleanupOnlyForRetentionSources(t *testing.T) {
	sources := &fakeSources{due: []domain.Source{
		{ID: 1, RetentionAfter: 0},                   // no retention → index only
		{ID: 2, RetentionAfter: 30 * 24 * time.Hour}, // retention → index + cleanup
	}}
	enqueuer := &fakeEnqueuer{}
	sched := silentScheduler(sources, enqueuer)

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var cleanupForSource2 int
	for _, task := range enqueuer.tasks {
		if task.Type == jobs.TaskCleanup {
			cleanupForSource2++
			if task.SourceID == nil || *task.SourceID != 2 {
				t.Errorf("cleanup task should target source 2, got %v", task.SourceID)
			}
		}
	}
	if cleanupForSource2 != 1 {
		t.Errorf("cleanup tasks = %d, want exactly 1 (only the retention source)", cleanupForSource2)
	}
}

func TestTickWithNoDueSourcesEnqueuesNoSourceWork(t *testing.T) {
	sources := &fakeSources{due: nil}
	enqueuer := &fakeEnqueuer{}
	sched := silentScheduler(sources, enqueuer)

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	for _, task := range enqueuer.tasks {
		if task.SourceID != nil {
			t.Errorf("enqueued source-scoped %s task with no due sources", task.Type)
		}
	}
}

func TestTickPrunesQueueHistoryOnItsOwnCadence(t *testing.T) {
	sources := &fakeSources{due: nil}
	enqueuer := &fakeEnqueuer{}
	sched := silentScheduler(sources, enqueuer)
	ctx := context.Background()

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if got := countOfType(enqueuer.tasks, jobs.TaskPruneJobs); got != 1 {
		t.Fatalf("first tick enqueued %d prune tasks, want 1", got)
	}

	// The scheduler wakes every minute; history is trimmed far less often, or the
	// queue fills with prune tasks.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("second Tick() error = %v", err)
	}
	if got := countOfType(enqueuer.tasks, jobs.TaskPruneJobs); got != 1 {
		t.Errorf("after a second tick there were %d prune tasks, want still 1", got)
	}
}

// countOfType returns how many enqueued tasks have the given type.
func countOfType(tasks []jobs.Task, taskType jobs.TaskType) int {
	count := 0
	for _, task := range tasks {
		if task.Type == taskType {
			count++
		}
	}
	return count
}
