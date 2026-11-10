package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake yt-dlp control. TestMain turns the test binary into a stand-in yt-dlp
// when fakeModeEnv is set, so ExecRunner's retry orchestration is exercised
// against a real subprocess without needing yt-dlp installed.
const (
	// fakeModeEnv selects the fake's behaviour; empty runs the tests normally.
	fakeModeEnv = "SUBSCRIBE_TEST_FAKE_YTDLP"
	// fakeOutputEnv is the file path the fake "downloads" (prints as moved).
	fakeOutputEnv = "SUBSCRIBE_TEST_FAKE_OUTPUT"

	// modeDenyPinned 403s any invocation pinning a player client and succeeds
	// without the pin — YouTube blocking tv_simply while the default works.
	modeDenyPinned = "deny-pinned"
	// modeDenyAll 403s every invocation — the block is not client-specific.
	modeDenyAll = "deny-all"

	fake403Line = "ERROR: unable to download video data: HTTP Error 403: Forbidden"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(runFakeYtDlp(mode))
	}
	os.Exit(m.Run())
}

// runFakeYtDlp is the fake's whole behaviour: refuse by mode, or report the
// pre-created output file the way yt-dlp's after_move --print lines do.
func runFakeYtDlp(mode string) int {
	pinned := false
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, playerClientArgPrefix) {
			pinned = true
		}
	}
	if mode == modeDenyAll || (mode == modeDenyPinned && pinned) {
		fmt.Fprintln(os.Stderr, fake403Line)
		return 1
	}
	fmt.Println(afterMovePrintPrefix + os.Getenv(fakeOutputEnv))
	fmt.Println(heightPrintPrefix + "1080")
	return 0
}

// fakeDownloadSetup points an ExecRunner at this test binary acting as yt-dlp
// in the given mode, and returns options plus the file the fake will report.
func fakeDownloadSetup(t *testing.T, mode string) (*ExecRunner, DownloadOptions, string) {
	t.Helper()
	home := t.TempDir()
	output := filepath.Join(home, "Chan", "Video [abc].mkv")
	writeFile(t, output)
	t.Setenv(fakeModeEnv, mode)
	t.Setenv(fakeOutputEnv, output)
	runner := NewExecRunner(os.Args[0], "", Throttle{})
	// OutputPath deliberately names a file that does not exist, so the
	// existing-download shortcut stays out of the way of these tests.
	opts := DownloadOptions{OutputPath: filepath.Join("Chan", "Fresh Video [new]"), HomeDir: home}
	return runner, opts, output
}

func TestDownloadRetriesWithoutThePinnedClientOnA403(t *testing.T) {
	runner, opts, output := fakeDownloadSetup(t, modeDenyPinned)

	result, err := runner.Download(context.Background(), "https://example.com/v", opts, nil)
	if err != nil {
		t.Fatalf("Download() = %v, want the fallback client to succeed", err)
	}
	if result.FilePath != output {
		t.Errorf("FilePath = %q, want %q from the unpinned run", result.FilePath, output)
	}
	if result.Height != 1080 {
		t.Errorf("Height = %d, want 1080 from the unpinned run", result.Height)
	}
}

func TestDownloadBlockedUnderEveryClientIsAccessDenied(t *testing.T) {
	runner, opts, _ := fakeDownloadSetup(t, modeDenyAll)

	_, err := runner.Download(context.Background(), "https://example.com/v", opts, nil)
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("Download() = %v, want ErrAccessDenied once both clients are refused", err)
	}
}

func TestDownloadAdoptsAFinishedFileWithoutInvokingYtDlp(t *testing.T) {
	// yt-dlp asked to re-download a finished file skips the transfer but still
	// re-runs --embed-thumbnail on it in place; the temp-rename that involves
	// is what destroyed a finished download on a network mount. The binary
	// path here does not exist, so this passes only if yt-dlp is never run.
	home := t.TempDir()
	relative := filepath.Join("Chan", "Finished Video [abc]")
	finished := filepath.Join(home, relative+".mkv")
	writeFile(t, finished)
	runner := NewExecRunner(filepath.Join(home, "no-such-binary"), "", Throttle{})

	result, err := runner.Download(context.Background(), "https://example.com/v",
		DownloadOptions{OutputPath: relative, HomeDir: home}, nil)
	if err != nil {
		t.Fatalf("Download() = %v, want the file on disk adopted without running yt-dlp", err)
	}
	if result.FilePath != finished {
		t.Errorf("FilePath = %q, want %q", result.FilePath, finished)
	}
	if result.FileSize == 0 {
		t.Error("FileSize = 0, want the existing file's size")
	}
}

func TestClassifyErrorMarksABare403AsAccessDenied(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		wantDenied bool
	}{
		{
			name:       "bare 403 on the video data",
			stderr:     fake403Line,
			wantDenied: true,
		},
		{
			// A bot-check page can carry a 403 alongside it; that is throttling,
			// and hammering on with another client would make it worse.
			name:       "403 wrapped in a bot check stays throttled",
			stderr:     "ERROR: HTTP Error 403: Forbidden. Sign in to confirm you're not a bot.",
			wantDenied: false,
		},
		{
			name:       "403 on a members-only video stays unavailable",
			stderr:     "ERROR: HTTP Error 403: Forbidden. Join this channel to get access to members-only content.",
			wantDenied: false,
		},
		{
			name:       "an ordinary failure is not an access denial",
			stderr:     "ERROR: unable to download video data: <urlopen error timed out>",
			wantDenied: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("exit status 1")
			got := classifyError(cause, test.stderr)
			if errors.Is(got, ErrAccessDenied) != test.wantDenied {
				t.Fatalf("ErrAccessDenied = %v, want %v (err: %v)",
					errors.Is(got, ErrAccessDenied), test.wantDenied, got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("classified error lost its cause: %v", got)
			}
		})
	}
}

func TestShouldRetryWithFallbackClient(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"bare 403", fake403Line, true},
		{"rate limit", "ERROR: HTTP Error 429: Too Many Requests", false},
		{"bot check with 403", "HTTP Error 403. Sign in to confirm you're not a bot.", false},
		{"members-only with 403", "HTTP Error 403: Forbidden. Members-only content.", false},
		{"ordinary failure", "ERROR: <urlopen error timed out>", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRetryWithFallbackClient(test.stderr); got != test.want {
				t.Errorf("shouldRetryWithFallbackClient(%q) = %v, want %v", test.stderr, got, test.want)
			}
		})
	}
}

func TestFallbackDownloadArgsDropOnlyThePlayerClientPin(t *testing.T) {
	opts := DownloadOptions{OutputPath: "Chan/video", HomeDir: "/media"}
	pinned := buildDownloadArgs("https://example.com/v", opts, "", Throttle{})
	fallback := buildDownloadArgsForClient("https://example.com/v", opts, "", Throttle{}, fallbackPlayerClient)

	pin := playerClientArgPrefix + defaultPlayerClient
	if !containsPair(pinned, flagExtractorArgs, pin) {
		t.Errorf("default download args lost the player-client pin: %v", pinned)
	}
	if containsPair(fallback, flagExtractorArgs, pin) {
		t.Errorf("fallback args must not pin the player client: %v", fallback)
	}
	// Everything except the pin must be identical, or the retry would change
	// more than the client and mask which difference fixed the download.
	if want, got := len(pinned)-2, len(fallback); got != want {
		t.Errorf("fallback args length = %d, want %d (pinned minus the pin pair)", got, want)
	}
}
