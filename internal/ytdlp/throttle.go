package ytdlp

import (
	"strconv"
	"time"
)

// yt-dlp pacing flags. These make a single invocation behave itself; the shared
// pacer in the pacing package is what keeps several invocations from behaving
// badly together.
const (
	flagSleepRequests = "--sleep-requests"
	flagLimitRate     = "--limit-rate"
)

// Throttle is how hard sub_scribe is willing to lean on the provider within and
// between yt-dlp runs.
//
// This exists because archiving with your own account's cookies puts that
// account at risk: a signed-in client that fetches without pause looks like
// automation, and the penalty is losing the account rather than losing a
// download. Every field's zero value disables that particular measure, so the
// zero Throttle is unrestricted behaviour.
//
// The interval between downloads is deliberately NOT here. That gap is long
// enough that waiting for it belongs in the task queue, where a waiting item
// costs a row rather than a worker; see library.DownloadPacer.
type Throttle struct {
	// RequestDelay pauses between individual HTTP requests within one yt-dlp run
	// (--sleep-requests). This is the one that covers listing and metadata
	// fetches, which are far more numerous than downloads.
	RequestDelay time.Duration

	// RateLimit caps download bandwidth in yt-dlp's own notation ("4.2M", "500K").
	// Empty means unlimited. Beyond politeness this smooths traffic out, so a
	// download looks less like a grab and more like someone watching.
	RateLimit string

	// CallGap is the minimum spacing between yt-dlp launches, enforced in this
	// process. The flags above only pace a run against itself, so with several
	// workers they would still start simultaneously; this stops that burst. It is
	// short by design — a caller waits it out rather than being rescheduled.
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

// appendDownloadFlags adds the request pacing plus the bandwidth cap, which only
// means anything when something is actually being transferred.
func (t Throttle) appendDownloadFlags(args []string) []string {
	args = t.appendRequestFlags(args)
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
