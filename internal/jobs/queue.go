package jobs

import (
	"context"
	"time"
)

// Queue is the persistence contract the worker pool depends on. The store package
// provides the concrete implementation; defining the interface here (where it is
// consumed) keeps the workers free of any database knowledge and trivially
// fakeable in tests.
type Queue interface {
	// Claim atomically returns the next runnable task, or (nil, nil) if none.
	Claim(ctx context.Context, now time.Time) (*Task, error)
	// Complete marks a task succeeded.
	Complete(ctx context.Context, id int64, now time.Time) error
	// Fail records a failed attempt, requeuing with backoff or marking failed.
	Fail(ctx context.Context, task Task, cause string, now time.Time) error
	// Defer returns a claimed task to the queue to run no earlier than runAfter,
	// without recording an attempt. It is how a handler declines its turn — see
	// Deferral — so waiting costs a queue row rather than a worker.
	Defer(ctx context.Context, id int64, runAfter, now time.Time, reason string) error
}

// Clock supplies the current time. Injecting it (rather than calling time.Now
// directly) makes scheduling and backoff deterministic under test.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock backed by the wall clock.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
