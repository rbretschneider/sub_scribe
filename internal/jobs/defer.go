package jobs

import (
	"fmt"
	"time"
)

// Deferral is a handler saying "not yet, ask me again later". It is returned as
// an error because that is how a handler reports an outcome, but it is not a
// failure: the task goes back to pending with a new eligibility time and its
// retry budget untouched.
//
// This is what lets work be spaced out over hours without a worker sitting idle
// for hours. A handler that slept instead would hold one of a small number of
// workers, so a queue of paced downloads would block indexing, feed generation,
// and anything the user asked for from the UI.
type Deferral struct {
	// RunAfter is when the task becomes eligible again.
	RunAfter time.Time
	// Reason is shown in the log and the job's detail view, so a task sitting in
	// the queue explains itself rather than looking stuck.
	Reason string
}

// Error describes the deferral, for logs and the job detail view.
func (d *Deferral) Error() string {
	return fmt.Sprintf("deferred until %s: %s", d.RunAfter.Format(time.RFC3339), d.Reason)
}

// Defer returns a Deferral asking for this task to be retried at runAfter
// without counting an attempt.
func Defer(runAfter time.Time, reason string) error {
	return &Deferral{RunAfter: runAfter, Reason: reason}
}
