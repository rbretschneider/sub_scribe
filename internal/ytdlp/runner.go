// Package ytdlp is the boundary between sub_scribe and the yt-dlp binary. It
// defines the Runner interface the rest of the app depends on so services can be
// tested against a fake, and provides the real subprocess-backed implementation.
package ytdlp

import (
	"context"
	"errors"
	"time"

	"sub_scribe/internal/domain"
)

// ErrFilteredOut reports that yt-dlp skipped an item rather than downloading it —
// for example because it was uploaded before the DateAfter cutoff. It is a
// normal outcome (the item should be marked skipped), not a failure to retry.
var ErrFilteredOut = errors.New("ytdlp: item skipped by filter")

// ErrThrottled reports that the provider is rate-limiting or bot-checking this
// client (HTTP 429, "confirm you're not a bot"). The item itself is fine —
// asking again later will work — but asking again immediately is what turns a
// slow-down into an account flag, so callers should back off, not retry.
var ErrThrottled = errors.New("ytdlp: provider is throttling this client")

// ErrUnavailable reports that the provider will not serve this item to us at all:
// it is members-only, private, removed, or otherwise permanently out of reach.
// Retrying cannot help, so callers should record it and move on rather than
// spending the task's retry budget. Wrapped errors carry yt-dlp's own wording.
var ErrUnavailable = errors.New("ytdlp: item unavailable")

// IndexEntry is one item discovered while indexing a source's collection. It maps
// directly onto domain.MediaMetadata at the service layer; keeping a dedicated
// DTO here stops domain types from leaking yt-dlp's JSON shape.
type IndexEntry struct {
	ExternalID   string
	Title        string
	Description  string
	Uploader     string
	UploadDate   time.Time
	Duration     time.Duration
	IsShort      bool
	IsLivestream bool
}

// IndexOptions controls how a collection is enumerated.
type IndexOptions struct {
	// CookiesPath is the cookies.txt to pass, or empty to omit cookies.
	CookiesPath string

	// DateAfter, when set (YYYYMMDD), limits the scan to items published on or
	// after that date and stops walking the collection at the first older one.
	//
	// This is the difference between a first scan of a year-old channel costing
	// seconds and costing hours. Without it the scan is a shallow listing that
	// carries no upload dates, so every item has to be recorded, queued, and then
	// looked up individually just to discover it is too old to want. With it the
	// provider returns dated entries and the walk stops at the window's edge.
	//
	// It assumes the collection is ordered newest-first, which is how channel and
	// playlist listings come back. An empty value scans everything, which is what
	// a source with no date window needs.
	DateAfter string
}

// DownloadOptions carries everything the downloader needs for one item. It is
// assembled by the service layer from a MediaProfile, so this package never reads
// domain configuration directly.
type DownloadOptions struct {
	// Format is the yt-dlp -f selector, e.g. "bestvideo[height<=1080]+bestaudio".
	Format string
	// OutputPath is the destination path without file extension; yt-dlp appends
	// the correct extension for the chosen format. It is relative to HomeDir when
	// that is set, and absolute otherwise.
	OutputPath string
	// HomeDir is the root the finished file is written under. It must be set for
	// TempDir to have any effect: yt-dlp ignores --paths entirely when the output
	// template is an absolute path, so the destination has to be expressed as
	// home + relative template instead.
	HomeDir string
	// TempDir, when set, is where yt-dlp keeps partial downloads and
	// postprocessing scratch files. Keeping that churn off the destination volume
	// is what makes downloads survive mounts that handle rename poorly. Empty
	// uses yt-dlp's default, which is alongside the output file.
	TempDir   string
	AudioOnly bool

	EmbedMetadata  bool
	EmbedThumbnail bool
	// WriteThumbnail saves the cover art as a JPEG alongside the media file,
	// sharing its base name — the layout media servers read as artwork.
	WriteThumbnail bool
	EmbedSubtitles bool
	SubtitleLangs  []string

	// SponsorBlockArgs are pre-built yt-dlp arguments from the sponsorblock
	// package, or empty when disabled.
	SponsorBlockArgs []string

	// ExtraArgs are raw user-supplied yt-dlp arguments appended verbatim.
	ExtraArgs []string

	// DateAfter, when set (YYYYMMDD), tells yt-dlp to skip videos uploaded before
	// this date. yt-dlp knows the real upload date at download time, so this is
	// where the source's cutoff is actually enforced.
	DateAfter string

	// CookiesPath is the cookies.txt to pass, or empty to omit cookies.
	CookiesPath string
}

// DownloadResult reports where the finished media landed and its size.
type DownloadResult struct {
	FilePath string
	FileSize int64
}

// ProgressFunc receives download progress as a percentage in [0,100]. It may be
// called many times and must be cheap and non-blocking.
type ProgressFunc func(percent float64)

// Runner executes yt-dlp operations. Implementations must be safe for concurrent
// use so multiple workers can index and download in parallel.
type Runner interface {
	// Index enumerates a collection's items without downloading, returning them in
	// the provider's natural order.
	Index(ctx context.Context, url string, opts IndexOptions) ([]IndexEntry, error)

	// Metadata fetches the full details of a single item. Indexing a collection is
	// deliberately shallow for speed, which leaves fields such as the upload date
	// blank; this fills them in for one item when they are actually needed.
	Metadata(ctx context.Context, url, cookiesPath string) (IndexEntry, error)

	// Download fetches a single item to opts.OutputPath, invoking onProgress as it
	// proceeds. onProgress may be nil.
	Download(ctx context.Context, url string, opts DownloadOptions, onProgress ProgressFunc) (DownloadResult, error)

	// Artwork fetches the imagery a collection publishes for itself — a channel's
	// avatar and banner — without enumerating its items. A collection that
	// publishes none yields an empty value rather than an error.
	Artwork(ctx context.Context, url, cookiesPath string) (domain.ChannelArtwork, error)
}
