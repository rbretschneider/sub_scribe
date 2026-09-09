package store

import (
	"context"
	"testing"
	"time"

	"sub_scribe/internal/jobs"
)

// TestDeferReschedulesWithoutConsumingAnAttempt covers the persistence half of
// the pacing design. Downloads are spaced out by returning the task to the queue
// with a later eligibility time, which only works if that does not look like a
// failed attempt — otherwise an item waiting its turn would exhaust its retry
// budget and be marked permanently failed without ever having been tried.
func TestDeferReschedulesWithoutConsumingAnAttempt(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	id, err := repo.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := repo.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim() = %v, %v", claimed, err)
	}
	attemptsWhenClaimed := claimed.Attempts

	runAfter := now.Add(10 * time.Minute)
	if err := repo.Defer(ctx, id, runAfter, now, "waiting its turn"); err != nil {
		t.Fatalf("Defer() error = %v", err)
	}

	// Still waiting, so a worker polling now finds nothing and moves on.
	if pending, err := repo.Claim(ctx, now); err != nil || pending != nil {
		t.Fatalf("Claim() during the wait = %v, %v; want nil (nothing to run)", pending, err)
	}

	again, err := repo.Claim(ctx, runAfter)
	if err != nil {
		t.Fatalf("Claim() after the wait error = %v", err)
	}
	if again == nil {
		t.Fatal("the deferred task never became eligible again")
	}
	if again.ID != id {
		t.Errorf("claimed task %d, want the deferred one (%d)", again.ID, id)
	}
	// A claim/defer round trip is attempts-neutral: the deferral gives back the
	// attempt the claim charged, so the re-claim lands on the same count the
	// first claim did. The earlier version of this test asserted +1 per cycle —
	// certifying a ratchet that let a task waiting through many pacing windows
	// arrive at its first real failure with no retry budget left.
	if again.Attempts != attemptsWhenClaimed {
		t.Errorf("attempts = %d after one deferral and one re-claim, want %d: waiting must not burn the retry budget",
			again.Attempts, attemptsWhenClaimed)
	}
	if again.LastError != "waiting its turn" {
		t.Errorf("LastError = %q, want the deferral reason so the job explains itself", again.LastError)
	}
}

// TestDeferSubSecondDeadlineWaitsOutTheFraction pins the fix for a claim/defer
// spin: run_after is stored in whole unix seconds, and truncating a deadline
// 300ms away produced a time already in the past — the task was instantly
// claimable again, and one waiting download racked up hundreds of phantom
// attempts per second until the fraction elapsed. A fractional deadline must
// round up, never down.
func TestDeferSubSecondDeadlineWaitsOutTheFraction(t *testing.T) {
	repo := newTestDB(t).Tasks()
	ctx := context.Background()

	id, err := repo.Enqueue(ctx, jobs.NewTask(jobs.TaskDownloadMedia, now))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if claimed, err := repo.Claim(ctx, now); err != nil || claimed == nil {
		t.Fatalf("Claim() = %v, %v", claimed, err)
	}

	if err := repo.Defer(ctx, id, now.Add(300*time.Millisecond), now, "waiting its turn"); err != nil {
		t.Fatalf("Defer() error = %v", err)
	}

	if spun, err := repo.Claim(ctx, now); err != nil || spun != nil {
		t.Fatalf("Claim() in the same second = %v, %v; want nil — this re-claim is the spin", spun, err)
	}
	eligible, err := repo.Claim(ctx, now.Add(time.Second))
	if err != nil || eligible == nil {
		t.Fatalf("Claim() after the next whole second = %v, %v; want the deferred task", eligible, err)
	}
	if eligible.ID != id {
		t.Errorf("claimed task %d, want %d", eligible.ID, id)
	}
}
