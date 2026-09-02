package ytdlp

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyErrorMarksPermanentFailures(t *testing.T) {
	tests := []struct {
		name            string
		stderr          string
		wantUnavailable bool
	}{
		{
			name:            "members-only video",
			stderr:          "ERROR: [youtube] cT_BYnPSlOM: Join this channel to get access to members-only content like this video, and other exclusive perks.",
			wantUnavailable: true,
		},
		{
			name:            "private video",
			stderr:          "ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video",
			wantUnavailable: true,
		},
		{
			name:            "removed video",
			stderr:          "ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader",
			wantUnavailable: true,
		},
		{
			name:            "age gate",
			stderr:          "ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.",
			wantUnavailable: true,
		},
		{
			name:            "matching is case-insensitive",
			stderr:          "ERROR: VIDEO UNAVAILABLE",
			wantUnavailable: true,
		},
		{
			name:            "transient network failure stays retryable",
			stderr:          "ERROR: unable to download video data: <urlopen error [Errno -3] Temporary failure in name resolution>",
			wantUnavailable: false,
		},
		{
			name:            "a warning about a JS runtime is not a permanent failure",
			stderr:          "WARNING: [youtube] No supported JavaScript runtime could be found.",
			wantUnavailable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("exit status 1")
			got := classifyError(cause, test.stderr)

			if errors.Is(got, ErrUnavailable) != test.wantUnavailable {
				t.Fatalf("ErrUnavailable = %v, want %v (err: %v)",
					errors.Is(got, ErrUnavailable), test.wantUnavailable, got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("classified error lost its cause: %v", got)
			}
		})
	}
}

func TestClassifyErrorMarksThrottling(t *testing.T) {
	tests := []struct {
		name          string
		stderr        string
		wantThrottled bool
	}{
		{
			name:          "http 429",
			stderr:        "ERROR: unable to download video data: HTTP Error 429: Too Many Requests",
			wantThrottled: true,
		},
		{
			name:          "bot check with a straight apostrophe",
			stderr:        "ERROR: [youtube] abc: Sign in to confirm you're not a bot. This helps protect our community.",
			wantThrottled: true,
		},
		{
			name:          "bot check with a typographic apostrophe",
			stderr:        "ERROR: [youtube] abc: Sign in to confirm you’re not a bot.",
			wantThrottled: true,
		},
		{
			name:          "an ordinary failure is not throttling",
			stderr:        "ERROR: unable to download video data: <urlopen error timed out>",
			wantThrottled: false,
		},
		{
			// The age gate also says "sign in", but it is about the video, not us —
			// it must stay classified as unavailable, not throttled.
			name:          "age gate stays unavailable",
			stderr:        "ERROR: [youtube] abc: Sign in to confirm your age.",
			wantThrottled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("exit status 1")
			got := classifyError(cause, test.stderr)

			if errors.Is(got, ErrThrottled) != test.wantThrottled {
				t.Fatalf("ErrThrottled = %v, want %v (err: %v)",
					errors.Is(got, ErrThrottled), test.wantThrottled, got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("classified error lost its cause: %v", got)
			}
		})
	}
}

func TestClassifyErrorPassesThroughNil(t *testing.T) {
	if err := classifyError(nil, "Join this channel to get access to members-only content"); err != nil {
		t.Fatalf("classifyError(nil) = %v, want nil", err)
	}
}

func TestClassifyErrorNamesTheReason(t *testing.T) {
	err := classifyError(errors.New("exit status 1"),
		"ERROR: Join this channel to get access to members-only content like this video")

	if !strings.Contains(err.Error(), "members-only content") {
		t.Fatalf("error %q does not explain why the item is unavailable", err)
	}
}

func TestBuildDownloadArgsKeepsScratchFilesOffTheDestination(t *testing.T) {
	args := buildDownloadArgs("https://example.com/v", DownloadOptions{
		OutputPath: "Chan/Season 2026/video",
		HomeDir:    "/media",
		TempDir:    "/var/tmp/sub_scribe",
	}, "", Throttle{})

	if !containsPair(args, flagPaths, "home:/media") {
		t.Errorf("expected --paths home:/media in %v", args)
	}
	if !containsPair(args, flagPaths, "temp:/var/tmp/sub_scribe") {
		t.Errorf("expected --paths temp:/var/tmp/sub_scribe in %v", args)
	}
	// yt-dlp discards --paths entirely for an absolute output template, so the
	// output must stay relative for the scratch directory to apply at all.
	if !containsPair(args, flagOutput, "Chan/Season 2026/video"+outputExtTemplate) {
		t.Errorf("output template must be relative to home, got %v", args)
	}
}

func TestBuildDownloadArgsOmitsPathsWhenNoHomeIsGiven(t *testing.T) {
	args := buildDownloadArgs("https://example.com/v",
		DownloadOptions{OutputPath: "/media/v", TempDir: "/var/tmp/sub_scribe"}, "", Throttle{})

	for _, arg := range args {
		if arg == flagPaths {
			t.Fatalf("--paths must be omitted for an absolute output, got %v", args)
		}
	}
}

func TestDatedIndexScanStopsAtTheWindowEdge(t *testing.T) {
	// A shallow listing carries no upload dates, so without a window every item
	// must be recorded and looked up individually just to learn it is too old. A
	// dated scan reports the dates up front and stops walking at the edge.
	args := buildIndexArgs("https://example.com/@chan", IndexOptions{DateAfter: "20260626"}, "", Throttle{})

	for _, want := range []string{flagDateAfter, "20260626", flagBreakOnReject, flagLazyPlaylist} {
		if !contains(args, want) {
			t.Errorf("expected %s in %v", want, args)
		}
	}
	// Flat mode is what withholds the dates, so it must not be combined with this.
	if contains(args, flagFlatPlaylist) {
		t.Errorf("a dated scan must not be flat, got %v", args)
	}
}

func TestUndatedIndexScanStaysShallowAndCheap(t *testing.T) {
	// With no window every item is wanted, so the fast shallow listing is right
	// and the dates are filled in at download time.
	args := buildIndexArgs("https://example.com/@chan", IndexOptions{}, "", Throttle{})

	if !contains(args, flagFlatPlaylist) {
		t.Errorf("expected a flat scan when there is no window, got %v", args)
	}
	for _, unwanted := range []string{flagDateAfter, flagBreakOnReject} {
		if contains(args, unwanted) {
			t.Errorf("did not expect %s without a window, got %v", unwanted, args)
		}
	}
}

func TestBuildMetadataArgsRequestsOneItemWithoutDownloading(t *testing.T) {
	args := buildMetadataArgs("https://example.com/v", "/config/cookies.txt", "", Throttle{})

	for _, want := range []string{flagDumpJSON, flagNoPlaylist, flagSkipDownload} {
		if !contains(args, want) {
			t.Errorf("expected %s in %v", want, args)
		}
	}
	if contains(args, flagFlatPlaylist) {
		t.Errorf("metadata fetch must not be flat, got %v", args)
	}
	if !containsPair(args, flagCookies, "/config/cookies.txt") {
		t.Errorf("expected cookies to be passed, got %v", args)
	}
}

// contains reports whether args includes want.
func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// containsPair reports whether args includes flag immediately followed by value.
func containsPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
