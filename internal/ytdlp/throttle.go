package ytdlp

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// yt-dlp pacing flags. These make a single invocation behave itself; the pacer
// below is what keeps several invocations from behaving badly together.
const (
	flagSleepRequests    = "--sleep-requests"
	flagSleepInterval    = "--sleep-interval"
	flagMaxSleepInterval = "--max-sleep-interval"
	flagLimitRate        = "--limit-rate"
)

// Throttle is how hard sub_scribe is willing to lean on the provider.
//
// This exists because archiving with your own account's cookies puts that
// account at risk: a signed-in client that fetches hundreds of videos back to
// back looks like automation, and the penalty is losing the account rather than
// losing a download. Every field's zero value disables that particular measure,
// so the zero Throttle is the old unrestricted behaviour.
type Throttle struct {
	// RequestDelay pauses between individual HTTP requests within one yt-dlp run
	// (--sleep-requests). This is the one that covers metadata and page fetches,
	// which are far more numerous than downloads.
	RequestDelay time.Duration

	// MinDownloadDelay and MaxDownloadDelay bound a random pause taken before
	// each download (--sleep-interval / --max-sleep-interval). It is randomised
	// on purpose: a perfectly regular gap is itself a signature. A max below the
	// min is ignored, leaving a fixed pause of exactly the minimum.
	MinDownloadDelay time.Duration
	MaxDownloadDelay time.Duration

	// RateLimit caps download bandwidth in yt-dlp's own notation ("4.2M", "500K").
	// Empty means unlimited. Beyond politeness this smooths traffic out, so an
	// archive run looks less like a burst and more like someone watching.
	RateLimit string

	// CallGap is the minimum spacing between yt-dlp launches, enforced in this
	// process. The sleep flags above only pace a run against itself, so with
	// several workers running they would still start simultaneously and hit the
	// provider in lockstep; this is what actually bounds the overall rate.
	CallGap time.Duration
}

// appendRequestFlags adds the pacing that applies to every kind of call,
// including the metadata and listing fetches that make up most of the traffic.
func (t Throttle) appendRequestFlags(args []string) []string {
	if t.RequestDelay <= 0 {
		return args
	}
	return append(args, flagSleepRequests, secondsArg(t.RequestDelay))
}

// appendDownloadFlags adds the pacing that only makes sense around a download:
// the pre-download pause and the bandwidth cap.
func (t Throttle) appendDownloadFlags(args []string) []string {
	args = t.appendRequestFlags(args)
	if t.MinDownloadDelay > 0 {
		args = append(args, flagSleepInterval, secondsArg(t.MinDownloadDelay))
		// yt-dlp rejects a maximum without a minimum, and treats a maximum below
		// the minimum as a mistake — so it is only passed when it widens the range.
		if t.MaxDownloadDelay > t.MinDownloadDelay {
			args = append(args, flagMaxSleepInterval, secondsArg(t.MaxDownloadDelay))
		}
	}
	if t.RateLimit != "" {
		args = append(args, flagLimitRate, t.RateLimit)
	}
	return args
}

// secondsArg renders a duration as the plain number of seconds yt-dlp expects,
// without a trailing ".0" for whole values.
func secondsArg(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// pacer spaces out the start of yt-dlp invocations across the whole process.
//
// Slots are handed out in advance rather than by sleeping under a lock: callers
// reserve the next free moment, then wait for it on their own. That way N
// waiting workers each get a distinct slot instead of all waking at once and
// racing, and a cancelled caller does not hold the queue up.
type pacer struct {
	mu   sync.Mutex
	gap  time.Duration
	next time.Time
	// now is the clock, injected so the spacing can be tested without waiting.
	now func() time.Time
}

// newPacer returns a pacer enforcing gap between calls. A gap of zero or less
// makes wait a no-op.
func newPacer(gap time.Duration) *pacer {
	return &pacer{gap: gap, now: time.Now}
}

// reserve claims the next available start time and advances the queue.
func (p *pacer) reserve() time.Time {
	now := p.now()
	if p.gap <= 0 {
		return now
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	at := now
	if p.next.After(now) {
		at = p.next
	}
	p.next = at.Add(p.gap)
	return at
}

// wait blocks until this caller's turn, or until ctx is done. It returns the
// context's error if the wait was cut short, so a shutdown does not silently
// turn into an unthrottled call.
func (p *pacer) wait(ctx context.Context) error {
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
