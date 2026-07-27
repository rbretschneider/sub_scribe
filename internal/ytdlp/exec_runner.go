package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecRunner is the subprocess-backed Runner. It shells out to a yt-dlp binary
// for indexing and downloading.
type ExecRunner struct {
	binaryPath string
	// potProviderURL is the base URL of an optional PO-token provider. When set,
	// it is passed to yt-dlp for both indexing and downloading so most YouTube
	// content can be fetched without cookies. Empty disables it.
	potProviderURL string
	// throttle is how gently to treat the provider. Its flags are added to every
	// argument list; its call gap is enforced by pacer.
	throttle Throttle
	// pacer spaces out invocations across all workers. Every method waits on it
	// before starting a process, so the configured gap bounds the whole app's
	// request rate rather than each worker's.
	pacer *pacer
}

var _ Runner = (*ExecRunner)(nil)

// NewExecRunner returns an ExecRunner that invokes the yt-dlp binary at
// binaryPath. potProviderURL may be empty to disable PO-token support, and a
// zero Throttle disables pacing entirely.
func NewExecRunner(binaryPath, potProviderURL string, throttle Throttle) *ExecRunner {
	return &ExecRunner{
		binaryPath:     binaryPath,
		potProviderURL: potProviderURL,
		throttle:       throttle,
		pacer:          newPacer(throttle.CallGap),
	}
}

// Index enumerates a collection's items by running yt-dlp with --dump-json and
// parsing each emitted JSON line. Blank and unparseable lines are logged and
// skipped rather than failing the whole index.
func (r *ExecRunner) Index(ctx context.Context, url string, opts IndexOptions) ([]IndexEntry, error) {
	if err := r.pacer.wait(ctx); err != nil {
		return nil, fmt.Errorf("yt-dlp index %q: %w", url, err)
	}
	cmd := exec.CommandContext(ctx, r.binaryPath, buildIndexArgs(url, opts, r.potProviderURL, r.throttle)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && !isEarlyStop(err) {
		return nil, fmt.Errorf("yt-dlp index %q: %w: %s", url, err, stderr.String())
	}
	return scanIndexEntries(&stdout), nil
}

// exitCodeEarlyStop is what yt-dlp returns when it stops walking a collection
// because it was told to — here, on reaching an item outside the date window.
const exitCodeEarlyStop = 101

// isEarlyStop reports whether a failure is really yt-dlp saying "I stopped where
// you asked me to". That is the successful outcome of a dated scan, and the
// entries gathered before the stop are on stdout as usual; treating it as an
// error would throw away the whole scan.
func isEarlyStop(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == exitCodeEarlyStop
}

// Metadata fetches one item's full details with a non-flat --dump-json, which is
// what supplies fields (notably the upload date) that the fast collection index
// leaves blank.
func (r *ExecRunner) Metadata(ctx context.Context, url, cookiesPath string) (IndexEntry, error) {
	if err := r.pacer.wait(ctx); err != nil {
		return IndexEntry{}, fmt.Errorf("yt-dlp metadata %q: %w", url, err)
	}
	cmd := exec.CommandContext(ctx, r.binaryPath, buildMetadataArgs(url, cookiesPath, r.potProviderURL, r.throttle)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return IndexEntry{}, classifyError(fmt.Errorf("yt-dlp metadata %q: %w: %s", url, err, stderr.String()), stderr.String())
	}
	entries := scanIndexEntries(&stdout)
	if len(entries) == 0 {
		return IndexEntry{}, fmt.Errorf("yt-dlp metadata %q: no entry returned", url)
	}
	return entries[0], nil
}

// scanIndexEntries parses each non-blank line of yt-dlp JSON output, skipping
// and logging lines that fail to parse.
func scanIndexEntries(r io.Reader) []IndexEntry {
	entries := make([]IndexEntry, 0)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBuffer), maxScanBuffer)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		entry, err := parseIndexLine(line)
		if err != nil {
			log.Printf("ytdlp: skipping unparseable index line: %v", err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// Scanner buffer sizes; yt-dlp descriptions can produce long JSON lines.
const (
	initialScanBuffer = 64 * 1024
	maxScanBuffer     = 4 * 1024 * 1024
)

// Download fetches one item, streaming progress to onProgress and capturing the
// final file path yt-dlp prints after moving the completed download.
func (r *ExecRunner) Download(ctx context.Context, url string, opts DownloadOptions, onProgress ProgressFunc) (DownloadResult, error) {
	if err := r.pacer.wait(ctx); err != nil {
		return DownloadResult{}, fmt.Errorf("yt-dlp download %q: %w", url, err)
	}
	cmd := exec.CommandContext(ctx, r.binaryPath, buildDownloadArgs(url, opts, r.potProviderURL, r.throttle)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return DownloadResult{}, fmt.Errorf("yt-dlp download %q: stdout pipe: %w", url, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return DownloadResult{}, fmt.Errorf("yt-dlp download %q: stderr pipe: %w", url, err)
	}
	if err := cmd.Start(); err != nil {
		return DownloadResult{}, fmt.Errorf("yt-dlp download %q: start: %w", url, err)
	}

	// yt-dlp's warnings and errors are the most useful thing in a job's log, so
	// they are surfaced line by line as they happen and also kept for the error
	// message if the run fails.
	var stderr bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		relayStderr(ctx, stderrPipe, &stderr)
	}()

	filePath := scanDownloadOutput(stdout, onProgress)
	<-stderrDone
	if err := cmd.Wait(); err != nil {
		return DownloadResult{}, classifyError(
			fmt.Errorf("yt-dlp download %q: %w: %s", url, err, stderr.String()), stderr.String())
	}
	return buildDownloadResult(url, filePath, opts)
}

// relayStderr logs each line yt-dlp writes to stderr as it arrives and copies it
// into keep for the eventual error message. Logging with the context means the
// lines are attributed to the job that is running, which is what fills the live
// log panel on a job's page.
func relayStderr(ctx context.Context, r io.Reader, keep *bytes.Buffer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBuffer), maxScanBuffer)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		keep.WriteString(line)
		keep.WriteByte('\n')
		if line == "" {
			continue
		}
		slog.Log(ctx, levelForOutput(line), "yt-dlp: "+line)
	}
}

// levelForOutput grades a yt-dlp output line so genuine errors stand out from
// the routine chatter in the log viewer.
func levelForOutput(line string) slog.Level {
	switch {
	case strings.HasPrefix(line, "ERROR:"):
		return slog.LevelError
	case strings.HasPrefix(line, "WARNING:"):
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// scanDownloadOutput reads yt-dlp's stdout, forwarding progress percentages to
// onProgress and returning the last after_move file path it printed.
func scanDownloadOutput(r io.Reader, onProgress ProgressFunc) string {
	var filePath string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBuffer), maxScanBuffer)
	for scanner.Scan() {
		line := scanner.Text()
		if percent, ok := parseProgressPercent(line); ok {
			if onProgress != nil {
				onProgress(percent)
			}
			continue
		}
		if path, ok := parseAfterMovePath(line); ok {
			filePath = path
		}
	}
	return filePath
}

// parseAfterMovePath extracts the final file path from an after_move print line.
func parseAfterMovePath(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, afterMovePrintPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, afterMovePrintPrefix)), true
}

// buildDownloadResult stats the downloaded file to report its size. It is called
// only after yt-dlp exits successfully.
//
// A clean exit with no moved-file path has two very different causes. yt-dlp may
// have declined the item (it failed the --dateafter cutoff), or the file may
// already have been on disk from an interrupted earlier attempt — in which case
// yt-dlp reports "has already been downloaded", skips the move postprocessor,
// and prints nothing. Treating the second case as a filter match is what turned
// successfully downloaded videos into "skipped" ones after every restart, so the
// destination is checked before that conclusion is drawn.
func buildDownloadResult(url, filePath string, opts DownloadOptions) (DownloadResult, error) {
	if filePath == "" {
		existing, ok := findExistingDownload(opts)
		if !ok {
			return DownloadResult{}, ErrFilteredOut
		}
		filePath = existing
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("yt-dlp download %q: stat output %q: %w", url, filePath, err)
	}
	return DownloadResult{FilePath: filePath, FileSize: info.Size()}, nil
}

// partialSuffixes mark yt-dlp's work-in-progress and sidecar files, which are
// never the finished download.
var partialSuffixes = []string{".part", ".ytdl", ".temp", ".webp", ".jpg", ".png", ".meta", ".nfo"}

// findExistingDownload looks for a finished file already sitting at the output
// path described by opts.
func findExistingDownload(opts DownloadOptions) (string, bool) {
	if opts.OutputPath == "" {
		return "", false
	}
	target := opts.OutputPath
	if opts.HomeDir != "" {
		target = filepath.Join(opts.HomeDir, opts.OutputPath)
	}
	return FindDownloadedFile(target)
}

// FindDownloadedFile reports the finished media file at basePath — a full path
// without an extension, since yt-dlp appends its own.
//
// It is exported so the application can also adopt files that are already on
// disk but missing from its records.
func FindDownloadedFile(basePath string) (string, bool) {
	dir := filepath.Dir(basePath)

	// A directory scan, not a glob: real titles contain "[", which glob would
	// read as a character class and silently fail to match.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	name, ok := MatchDownloadedFile(names, filepath.Base(basePath))
	if !ok {
		return "", false
	}
	return filepath.Join(dir, name), true
}

// MatchDownloadedFile picks the finished media file for baseName out of a
// directory listing, rejecting partial and sidecar files. It reports false
// unless exactly one candidate is found, because guessing between several is
// worse than downloading again.
//
// It takes a listing rather than reading the directory so a caller checking many
// items in the same folder — startup recovery walking a whole archive — can read
// that folder once instead of once per item.
func MatchDownloadedFile(names []string, baseName string) (string, bool) {
	var found string
	for _, name := range names {
		// The finished file is the base name plus exactly one extension. This also
		// rejects yt-dlp's intermediate per-format files, such as ".f399.mp4",
		// which would otherwise look like a second candidate and force a retry.
		if strings.TrimSuffix(name, filepath.Ext(name)) != baseName || isPartialFile(name) {
			continue
		}
		if found != "" {
			return "", false
		}
		found = name
	}
	return found, found != ""
}

// isPartialFile reports whether a filename is one of yt-dlp's intermediate or
// sidecar artifacts rather than the finished media file.
func isPartialFile(name string) bool {
	for _, suffix := range partialSuffixes {
		if strings.HasSuffix(name, suffix) || strings.Contains(name, suffix+".") {
			return true
		}
	}
	return false
}
