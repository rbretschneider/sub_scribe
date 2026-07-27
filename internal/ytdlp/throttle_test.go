package ytdlp

import (
	"context"
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
			// calls: yt-dlp would take the pre-download pause on every lookup.
			name:     "download settings are not applied to plain requests",
			throttle: Throttle{MinDownloadDelay: 5 * time.Second, RateLimit: "1M"},
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
			name:     "a range becomes both sleep bounds",
			throttle: Throttle{MinDownloadDelay: 3 * time.Second, MaxDownloadDelay: 12 * time.Second},
			want:     []string{"--sleep-interval", "3", "--max-sleep-interval", "12"},
		},
		{
			// yt-dlp rejects a maximum that is not above the minimum, so an
			// equal or inverted pair has to collapse to a fixed pause.
			name:     "a maximum below the minimum is dropped",
			throttle: Throttle{MinDownloadDelay: 10 * time.Second, MaxDownloadDelay: 4 * time.Second},
			want:     []string{"--sleep-interval", "10"},
		},
		{
			name:     "a maximum without a minimum is ignored",
			throttle: Throttle{MaxDownloadDelay: 9 * time.Second},
			want:     nil,
		},
		{
			name:     "rate limit passes through verbatim",
			throttle: Throttle{RateLimit: "4.2M"},
			want:     []string{"--limit-rate", "4.2M"},
		},
		{
			name: "everything together, request pacing included",
			throttle: Throttle{
				RequestDelay:     time.Second,
				MinDownloadDelay: 3 * time.Second,
				MaxDownloadDelay: 12 * time.Second,
				RateLimit:        "500K",
			},
			want: []string{
				"--sleep-requests", "1",
				"--sleep-interval", "3", "--max-sleep-interval", "12",
				"--limit-rate", "500K",
			},
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

// TestPacerSpacesReservations checks the property that matters: with several
// workers asking at once, each gets a distinct slot one gap after the last. A
// pacer that let them all through together would leave the provider seeing the
// same burst it sees with no throttle at all.
func TestPacerSpacesReservations(t *testing.T) {
	start := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	pacer := newPacer(5 * time.Second)
	pacer.now = func() time.Time { return start } // all callers arrive at once

	for i := range 4 {
		want := start.Add(time.Duration(i) * 5 * time.Second)
		if got := pacer.reserve(); !got.Equal(want) {
			t.Errorf("reservation %d = %v, want %v", i, got, want)
		}
	}
}

// TestPacerDoesNotDelayAnIdleCaller checks that a gap which has already elapsed
// costs nothing, so pacing only bites when calls actually bunch up.
func TestPacerDoesNotDelayAnIdleCaller(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	pacer := newPacer(5 * time.Second)
	pacer.now = func() time.Time { return now }

	pacer.reserve()
	now = now.Add(time.Hour) // a long quiet period
	if got := pacer.reserve(); !got.Equal(now) {
		t.Errorf("reservation after an idle period = %v, want %v (no wait)", got, now)
	}
}

func TestPacerZeroGapIsDisabled(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	pacer := newPacer(0)
	pacer.now = func() time.Time { return now }

	for range 3 {
		if got := pacer.reserve(); !got.Equal(now) {
			t.Errorf("reservation with no gap = %v, want %v", got, now)
		}
	}
}

// TestPacerWaitHonoursCancellation makes sure a shutdown interrupts the wait and
// is reported, rather than either hanging or quietly proceeding unthrottled.
func TestPacerWaitHonoursCancellation(t *testing.T) {
	pacer := newPacer(time.Hour)
	pacer.reserve() // claim the first slot so the next caller has to wait

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pacer.wait(ctx); err == nil {
		t.Fatal("wait() returned nil for a cancelled context; the call would proceed unthrottled")
	}
}

func TestPacerWaitReturnsImmediatelyWhenDisabled(t *testing.T) {
	if err := newPacer(0).wait(context.Background()); err != nil {
		t.Fatalf("wait() with no gap = %v, want nil", err)
	}
}

// TestThrottleReachesEveryKindOfCall is the check that a configured throttle is
// not quietly ignored. A pacing setting the user believes is protecting their
// account, but which never reaches the command line, is worse than none at all —
// so every call that touches the provider is asserted here, not just downloads.
func TestThrottleReachesEveryKindOfCall(t *testing.T) {
	throttle := Throttle{RequestDelay: time.Second, MinDownloadDelay: 3 * time.Second}

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
	// The pre-download pause belongs to downloads alone; on a listing it would
	// stall every item for no benefit.
	if !containsPair(calls["download"], flagSleepInterval, "3") {
		t.Errorf("download is missing its pre-download pause: %v", calls["download"])
	}
	if contains(calls["index"], flagSleepInterval) {
		t.Errorf("index must not take the pre-download pause: %v", calls["index"])
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
