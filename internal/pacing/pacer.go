// Package pacing rations access to a shared external service. It exists because
// sub_scribe archives with the user's own account credentials, where the cost of
// looking like a scraper is the account rather than a failed request.
//
// A Pacer is the single place that decides when the next call may start. Callers
// either wait for their turn (fine for short gaps) or ask whether it is their
// turn yet and come back later (the only sane option for long ones, since a
// worker asleep for ten minutes is a worker not doing anything else).
package pacing

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Gap reports how long to leave before the next start. It is called once per
// claimed slot, so an implementation may vary its answer — see Jittered.
type Gap func() time.Duration

// Fixed spaces calls evenly. A duration of zero or less disables pacing, which
// is how every setting is turned off.
func Fixed(d time.Duration) Gap {
	return func() time.Duration { return d }
}

// Jittered spaces calls by a random duration in [min, max].
//
// The randomness is the point, not a detail: a perfectly regular interval is
// itself a signature, and something requesting a video every 600.0 seconds does
// not look like a person. A max at or below min degrades to a fixed gap.
func Jittered(min, max time.Duration) Gap {
	if max <= min {
		return Fixed(min)
	}
	spread := max - min
	return func() time.Duration { return min + rand.N(spread) }
}

// Pacer hands out start times so that consecutive calls are separated by a gap.
// It is safe for concurrent use; several workers share one Pacer, which is what
// makes the spacing a property of the whole application rather than of each
// worker separately.
type Pacer struct {
	mu   sync.Mutex
	gap  Gap
	next time.Time
	// now is the clock, injected so pacing can be tested without waiting.
	now func() time.Time
}

// New returns a Pacer enforcing gap between starts. A nil gap disables pacing.
func New(gap Gap) *Pacer {
	if gap == nil {
		gap = Fixed(0)
	}
	return &Pacer{gap: gap, now: time.Now}
}

// TryClaim takes the next slot if it is free, reporting the start time and true.
// When it is not free it reports when the slot frees up and false, and takes
// nothing — so a caller that decides to come back later has not consumed the
// turn it did not use, and the queue does not drift ever further into the
// future as callers check on it.
func (p *Pacer) TryClaim() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.next.After(now) {
		return p.next, false
	}
	p.next = now.Add(p.gap())
	return now, true
}

// Wait blocks until this caller's turn, or until ctx is done, returning the
// context's error if the wait was cut short. Use it only for gaps short enough
// that holding the caller is cheaper than rescheduling it.
func (p *Pacer) Wait(ctx context.Context) error {
	delay := p.reserve().Sub(p.now())
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// reserve claims the next free slot unconditionally and advances the queue.
//
// Slots are handed out in advance rather than by sleeping under the lock so that
// several waiting callers each get a distinct slot, instead of all waking at the
// same moment and racing for one.
func (p *Pacer) reserve() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	at := now
	if p.next.After(now) {
		at = p.next
	}
	p.next = at.Add(p.gap())
	return at
}
