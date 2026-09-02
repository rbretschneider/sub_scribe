package ytdlp

import (
	"fmt"
	"strings"
)

// unavailableMarkers are fragments of yt-dlp's own error output that mean the
// provider will never serve this item to us with the current credentials. They
// are matched case-insensitively against stderr. Retrying any of these just
// burns the task's attempts and buries the real reason in a generic failure, so
// they are surfaced as ErrUnavailable instead.
var unavailableMarkers = []string{
	"join this channel to get access to members-only content",
	"this video is available to this channel's members",
	"video unavailable",
	"private video",
	"this video is private",
	"this video has been removed",
	"account associated with this video has been terminated",
	"sign in to confirm your age",
	"age-restricted video",
	"who has blocked it in your country",
	"not available in your country",
	"this live event has ended",
	"members-only content",
}

// throttledMarkers are fragments of yt-dlp's output that mean the provider is
// rate-limiting or bot-checking this client. Unlike unavailableMarkers these are
// about us, not the video: hammering on regardless is what escalates a temporary
// slow-down into an account flag, so they are surfaced as ErrThrottled and the
// caller backs off instead of burning retries.
var throttledMarkers = []string{
	"http error 429",
	"too many requests",
	"confirm you're not a bot",
	"confirm you’re not a bot",
	"rate-limited by youtube",
}

// classifyError tags a yt-dlp failure by what its output reveals: throttling
// (back off), or a permanent "we will never be allowed to fetch this" refusal
// (give up). Throttling is checked first because a bot-check page can carry
// other error text alongside it. The original error is always preserved as the
// cause; only the classification is added.
func classifyError(err error, stderr string) error {
	if err == nil {
		return nil
	}
	if reason, ok := matchMarker(stderr, throttledMarkers); ok {
		return fmt.Errorf("%w (%s): %w", ErrThrottled, reason, err)
	}
	if reason, ok := unavailableReason(stderr); ok {
		return fmt.Errorf("%w (%s): %w", ErrUnavailable, reason, err)
	}
	return err
}

// unavailableReason reports which permanent-failure marker the output matched,
// returning the marker so the UI can show why an item was given up on.
func unavailableReason(stderr string) (string, bool) {
	return matchMarker(stderr, unavailableMarkers)
}

// matchMarker reports which of the given markers the output contains,
// case-insensitively.
func matchMarker(stderr string, markers []string) (string, bool) {
	lowered := strings.ToLower(stderr)
	for _, marker := range markers {
		if strings.Contains(lowered, marker) {
			return marker, true
		}
	}
	return "", false
}
