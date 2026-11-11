package jobs

import (
	"fmt"
	"time"
)

// Retry is a genuine failure that chooses when the next attempt may start,
// for errors whose cause takes hours to pass (a provider blocking a client
// identity) rather than the seconds-to-minutes the standard backoff assumes.
// Unlike a Deferral it spends an attempt, so a cause that never passes runs
// out of budget and settles as failed instead of circling forever.
type Retry struct {
	// RunAfter is when the next attempt becomes eligible.
	RunAfter time.Time
	// Cause is the failure being retried, stored on the task as its last error.
	Cause error
}

// Error describes the failure and the chosen comeback time.
func (r *Retry) Error() string {
	return fmt.Sprintf("retrying after %s: %v", r.RunAfter.Format(time.RFC3339), r.Cause)
}

// Unwrap exposes the cause so errors.Is sees through the scheduling wrapper.
func (r *Retry) Unwrap() error {
	return r.Cause
}

// RetryAfter returns a Retry failing this attempt and scheduling the next one
// no earlier than runAfter.
func RetryAfter(runAfter time.Time, cause error) error {
	return &Retry{RunAfter: runAfter, Cause: cause}
}
