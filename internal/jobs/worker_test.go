package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fixedClock is a deterministic Clock for tests.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// fakeQueue is an in-memory Queue that serves a single pending task and records
// the terminal call the worker makes, so tests assert on the contract (was it
// completed or failed?) rather than any internal state.
type fakeQueue struct {
	pending *Task

	completedID  int64
	wasCompleted bool

	failedCause string
	wasFailed   bool
}

func (q *fakeQueue) Claim(_ context.Context, _ time.Time) (*Task, error) {
	if q.pending == nil {
		return nil, nil
	}
	task := *q.pending
	q.pending = nil
	return &task, nil
}

func (q *fakeQueue) Complete(_ context.Context, id int64, _ time.Time) error {
	q.completedID = id
	q.wasCompleted = true
	return nil
}

func (q *fakeQueue) Fail(_ context.Context, _ Task, cause string, _ time.Time) error {
	q.failedCause = cause
	q.wasFailed = true
	return nil
}

// silentLogger discards log output so tests stay quiet.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newPoolWith wires a pool around a fake queue and a registry seeded with the
// given handler for TaskIndexSource.
func newPoolWith(queue Queue, handler Handler) *Pool {
	registry := NewRegistry()
	if handler != nil {
		registry.Register(TaskIndexSource, handler)
	}
	clock := fixedClock{t: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	return NewPool(queue, registry, clock, PoolConfig{Workers: 1, Logger: silentLogger()})
}

func indexTask() *Task {
	task := NewTask(TaskIndexSource, time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC))
	task.ID = 42
	return &task
}

func TestProcessOnceReportsNoWorkWhenQueueEmpty(t *testing.T) {
	pool := newPoolWith(&fakeQueue{}, HandlerFunc(func(context.Context, Task) error { return nil }))
	worked, err := pool.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if worked {
		t.Error("worked = true, want false for empty queue")
	}
}

func TestProcessOnceCompletesSuccessfulTask(t *testing.T) {
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error { return nil }))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if !queue.wasCompleted {
		t.Fatal("task was not completed")
	}
	if queue.completedID != 42 {
		t.Errorf("completedID = %d, want 42", queue.completedID)
	}
	if queue.wasFailed {
		t.Error("successful task should not be failed")
	}
}

func TestProcessOnceFailsErroringTask(t *testing.T) {
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		return errors.New("yt-dlp exploded")
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if !queue.wasFailed {
		t.Fatal("erroring task was not failed")
	}
	if queue.failedCause != "yt-dlp exploded" {
		t.Errorf("failedCause = %q, want the handler's error", queue.failedCause)
	}
}

func TestProcessOnceRecoversHandlerPanic(t *testing.T) {
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		panic("nil map write")
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() should recover panic, got error = %v", err)
	}
	if !queue.wasFailed {
		t.Error("panicking task should be recorded as failed, not lost")
	}
}

func TestProcessOnceFailsTaskWithNoRegisteredHandler(t *testing.T) {
	queue := &fakeQueue{pending: indexTask()}
	// Register a handler for a different type, leaving TaskIndexSource unhandled.
	registry := NewRegistry()
	registry.Register(TaskDownloadMedia, HandlerFunc(func(context.Context, Task) error { return nil }))
	clock := fixedClock{t: time.Now()}
	pool := NewPool(queue, registry, clock, PoolConfig{Workers: 1, Logger: silentLogger()})

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if !queue.wasFailed {
		t.Error("task with no handler should be failed, not silently dropped")
	}
}
