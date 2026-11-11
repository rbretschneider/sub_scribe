package jobs

import (
	"context"
	"errors"
	"fmt"
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

	failedAtCause    string
	failedAtRunAfter time.Time
	wasFailedAt      bool

	deferredID       int64
	deferredRunAfter time.Time
	deferredReason   string
	wasDeferred      bool
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

func (q *fakeQueue) FailAt(_ context.Context, _ Task, cause string, runAfter, _ time.Time) error {
	q.failedAtCause = cause
	q.failedAtRunAfter = runAfter
	q.wasFailedAt = true
	return nil
}

func (q *fakeQueue) Defer(_ context.Context, id int64, runAfter, _ time.Time, reason string) error {
	q.deferredID = id
	q.deferredRunAfter = runAfter
	q.deferredReason = reason
	q.wasDeferred = true
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

// TestDeferredTaskIsRescheduledNotFailed pins the distinction that makes long
// pacing intervals affordable: a handler declining its turn must not burn an
// attempt, or a download spaced over hours would exhaust its retry budget
// waiting and be marked permanently failed without ever being tried.
func TestDeferredTaskIsRescheduledNotFailed(t *testing.T) {
	runAfter := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		return Defer(runAfter, "waiting its turn")
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}

	if !queue.wasDeferred {
		t.Fatal("task was not deferred")
	}
	if queue.wasFailed {
		t.Error("a deferral was recorded as a failure, which would consume a retry")
	}
	if queue.wasCompleted {
		t.Error("a deferral was recorded as success, so the work would never run")
	}
	if queue.deferredID != 42 {
		t.Errorf("deferred id = %d, want 42", queue.deferredID)
	}
	if !queue.deferredRunAfter.Equal(runAfter) {
		t.Errorf("deferred run_after = %v, want %v", queue.deferredRunAfter, runAfter)
	}
	if queue.deferredReason != "waiting its turn" {
		t.Errorf("deferred reason = %q, want the handler's reason", queue.deferredReason)
	}
}

// TestScheduledRetryFailsAtItsChosenTime pins the third outcome a handler can
// report: a genuine failure that picks its own comeback time. It must spend
// the attempt (FailAt, not Defer) — that budget is the only thing bounding a
// failure whose cause may never pass — while honoring the chosen eligibility
// time instead of the standard backoff.
func TestScheduledRetryFailsAtItsChosenTime(t *testing.T) {
	runAfter := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		return RetryAfter(runAfter, errors.New("HTTP Error 403"))
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}

	if !queue.wasFailedAt {
		t.Fatal("scheduled retry was not recorded via FailAt")
	}
	if queue.wasDeferred {
		t.Error("a scheduled retry must not be a deferral, or it would never spend its budget")
	}
	if queue.wasFailed || queue.wasCompleted {
		t.Error("a scheduled retry must be recorded only via FailAt")
	}
	if !queue.failedAtRunAfter.Equal(runAfter) {
		t.Errorf("failedAt run_after = %v, want %v", queue.failedAtRunAfter, runAfter)
	}
	if queue.failedAtCause != "HTTP Error 403" {
		t.Errorf("failedAt cause = %q, want the underlying cause without the scheduling wrapper", queue.failedAtCause)
	}
}

// TestScheduledRetrySurvivesWrapping checks a wrapped Retry is still honored,
// since handlers routinely add context to what they return.
func TestScheduledRetrySurvivesWrapping(t *testing.T) {
	runAfter := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		return fmt.Errorf("download media 7: %w", RetryAfter(runAfter, errors.New("blocked")))
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if !queue.wasFailedAt || queue.wasFailed {
		t.Error("a wrapped scheduled retry was treated as a plain failure")
	}
}

// TestDeferralSurvivesWrapping checks that a deferral is still recognised after a
// caller adds context to it, since handlers routinely wrap what they return.
func TestDeferralSurvivesWrapping(t *testing.T) {
	runAfter := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	queue := &fakeQueue{pending: indexTask()}
	pool := newPoolWith(queue, HandlerFunc(func(context.Context, Task) error {
		return fmt.Errorf("download media 7: %w", Defer(runAfter, "paced"))
	}))

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if !queue.wasDeferred || queue.wasFailed {
		t.Error("a wrapped deferral was treated as a failure")
	}
}
