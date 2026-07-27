package ytdlp

import (
	"reflect"
	"testing"
	"time"
)

func TestThrottleRequestFlags(t *testing.T) {
	tests := []struct {
		name     string
		throttle Throttle
		want     []string
	}{
		{
			name:     "zero throttle adds nothing",
			throttle: Throttle{},
			want:     nil,
		},
		{
			name:     "whole seconds render without a decimal point",
			throttle: Throttle{RequestDelay: 2 * time.Second},
			want:     []string{"--sleep-requests", "2"},
		},
		{
			name:     "fractional seconds are preserved",
			throttle: Throttle{RequestDelay: 1500 * time.Millisecond},
			want:     []string{"--sleep-requests", "1.5"},
		},
		{
			// A download-only setting must not leak into listing and metadata
			// calls, where there is no transfer for it to apply to.
			name:     "the rate limit is not applied to plain requests",
			throttle: Throttle{RateLimit: "1M"},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.throttle.appendRequestFlags(nil)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("appendRequestFlags() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestThrottleDownloadFlags(t *testing.T) {
	tests := []struct {
		name     string
		throttle Throttle
		want     []string
	}{
		{
			name:     "zero throttle adds nothing",
			throttle: Throttle{},
			want:     nil,
		},
		{
			name:     "rate limit passes through verbatim",
			throttle: Throttle{RateLimit: "4.2M"},
			want:     []string{"--limit-rate", "4.2M"},
		},
		{
			name:     "request pacing applies to downloads too",
			throttle: Throttle{RequestDelay: time.Second, RateLimit: "500K"},
			want:     []string{"--sleep-requests", "1", "--limit-rate", "500K"},
		},
		{
			// CallGap is enforced in this process, not by yt-dlp; leaking it as a
			// flag would be an unknown-argument error.
			name:     "call gap is not a yt-dlp flag",
			throttle: Throttle{CallGap: 5 * time.Second},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.throttle.appendDownloadFlags(nil)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("appendDownloadFlags() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNoSleepFlagsBetweenDownloads pins a decision that is easy to undo by
// reflex. The interval between downloads is long, and yt-dlp's --sleep-interval
// would spend it inside a running process — holding one of very few workers for
// ten minutes and starving indexing, feeds, and anything the user just asked
// for. That wait belongs to the task queue instead.
func TestNoSleepFlagsBetweenDownloads(t *testing.T) {
	args := buildDownloadArgs("https://example.com/v", DownloadOptions{OutputPath: "out"}, "",
		Throttle{RequestDelay: time.Second, RateLimit: "1M", CallGap: 5 * time.Second})

	for _, forbidden := range []string{"--sleep-interval", "--max-sleep-interval"} {
		if contains(args, forbidden) {
			t.Errorf("%s makes a worker sleep through the interval; it belongs in the queue: %v",
				forbidden, args)
		}
	}
}

// TestThrottleReachesEveryKindOfCall is the check that a configured throttle is
// not quietly ignored. A pacing setting the user believes is protecting their
// account, but which never reaches the command line, is worse than none at all —
// so every call that touches the provider is asserted here, not just downloads.
func TestThrottleReachesEveryKindOfCall(t *testing.T) {
	throttle := Throttle{RequestDelay: time.Second}

	calls := map[string][]string{
		"index":    buildIndexArgs("https://example.com/@chan", IndexOptions{}, "", throttle),
		"metadata": buildMetadataArgs("https://example.com/v", "", "", throttle),
		"download": buildDownloadArgs("https://example.com/v", DownloadOptions{OutputPath: "out"}, "", throttle),
	}
	for name, args := range calls {
		if !containsPair(args, flagSleepRequests, "1") {
			t.Errorf("%s call is not paced: %v", name, args)
		}
	}
}

// TestUserArgumentsOverrideTheThrottle pins the precedence: yt-dlp takes the last
// occurrence of a flag, so a profile's own --limit-rate has to come after ours or
// it would be silently ignored.
func TestUserArgumentsOverrideTheThrottle(t *testing.T) {
	args := buildDownloadArgs("https://example.com/v", DownloadOptions{
		OutputPath: "out",
		ExtraArgs:  []string{flagLimitRate, "9M"},
	}, "", Throttle{RateLimit: "1M"})

	ours, theirs := indexOfValue(args, "1M"), indexOfValue(args, "9M")
	if ours < 0 || theirs < 0 {
		t.Fatalf("expected both rate limits present, got %v", args)
	}
	if theirs < ours {
		t.Errorf("the user's --limit-rate must come last to win, got %v", args)
	}
}

// indexOfValue returns the position of value in args, or -1 when absent.
func indexOfValue(args []string, value string) int {
	for i, arg := range args {
		if arg == value {
			return i
		}
	}
	return -1
}
