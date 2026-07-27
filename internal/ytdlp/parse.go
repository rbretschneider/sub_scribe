package ytdlp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// uploadDateLayout is yt-dlp's upload_date format (YYYYMMDD).
const uploadDateLayout = "20060102"

// shortsPathSegment marks a URL as a YouTube Short.
const shortsPathSegment = "/shorts/"

// progressLinePrefix is a marker of our own, embedded in the progress template
// so percentage lines can be told apart from yt-dlp's ordinary chatter.
//
// The same trap as afterMovePrintPrefix: in --progress-template the text before
// the colon selects *which* progress to report, it is not printed. The old
// template "download:%(progress._percent_str)s" emitted a bare "  12.3%", so
// matching on a "download:" prefix found nothing and no progress was ever
// reported for any download.
const progressLinePrefix = "subscribe-progress:"

// afterMovePrintPrefix is a marker of our own, embedded in the --print template
// so the final path can be picked out of yt-dlp's stdout unambiguously.
//
// It is easy to assume `--print after_move:filepath` emits an "after_move:"
// prefixed line. It does not: "after_move" selects *when* to print, and the
// output is the bare value. Matching on that imagined prefix silently found no
// path for every download ever made, which the caller then read as "yt-dlp
// declined this item" — so completed downloads were all recorded as skipped.
const afterMovePrintPrefix = "subscribe-filepath:"

// Live status values that indicate a livestream item.
const (
	liveStatusIsLive   = "is_live"
	liveStatusWasLive  = "was_live"
	liveStatusPostLive = "post_live"
)

// yt-dlp CLI flags used when building argument lists.
const (
	flagDumpJSON          = "--dump-json"
	flagFlatPlaylist      = "--flat-playlist"
	flagIgnoreErrors      = "--ignore-errors"
	flagCookies           = "--cookies"
	flagOutput            = "-o"
	flagExtractAudio      = "-x"
	flagFormat            = "-f"
	flagEmbedMetadata     = "--embed-metadata"
	flagEmbedThumbnail    = "--embed-thumbnail"
	flagWriteThumbnail    = "--write-thumbnail"
	flagConvertThumbnails = "--convert-thumbnails"
	// thumbnailFormat is what media servers expect a sidecar image to be, and
	// converting also normalises YouTube's .webp originals to one extension.
	thumbnailFormat = "jpg"
	flagEmbedSubs   = "--embed-subs"
	flagSubLangs    = "--sub-langs"
	flagNewline     = "--newline"
	// flagForceProgress is required because --print puts yt-dlp into quiet mode,
	// which otherwise silences progress entirely — the template is honoured but
	// nothing is ever emitted to parse.
	flagForceProgress    = "--progress"
	flagProgressTemplate = "--progress-template"
	flagPrint            = "--print"
	flagExtractorArgs    = "--extractor-args"
	flagDateAfter        = "--dateafter"
	flagPaths            = "--paths"
	flagNoPlaylist       = "--no-playlist"
	flagSkipDownload     = "--skip-download"
	// flagBreakOnReject stops the scan at the first item outside the date window,
	// and flagLazyPlaylist makes entries stream out as they are found so that stop
	// actually saves the rest of the walk.
	flagBreakOnReject = "--break-on-reject"
	flagLazyPlaylist  = "--lazy-playlist"
	tempPathPrefix    = "temp:"
	homePathPrefix    = "home:"
	outputExtTemplate = ".%(ext)s"
	// progressTemplateValue reports download progress tagged with our marker.
	progressTemplateValue = "download:" + progressLinePrefix + "%(progress._percent_str)s"
	// afterMovePrintValue prints the finished file's path, tagged with our marker,
	// once yt-dlp has moved it into place.
	afterMovePrintValue = "after_move:" + afterMovePrintPrefix + "%(filepath)s"
	subLangSeparator    = ","
	// potProviderArgPrefix is the yt-dlp extractor-args key for the bgutil HTTP
	// PO-token provider; the provider's base URL is appended to it.
	potProviderArgPrefix = "youtubepot-bgutilhttp:base_url="
)

// indexLine is the subset of yt-dlp --dump-json fields this package consumes.
type indexLine struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Uploader    string  `json:"uploader"`
	Channel     string  `json:"channel"`
	UploadDate  string  `json:"upload_date"`
	Duration    float64 `json:"duration"`
	WebpageURL  string  `json:"webpage_url"`
	URL         string  `json:"url"`
	LiveStatus  string  `json:"live_status"`
	WasLive     bool    `json:"was_live"`
}

// parseIndexLine parses one yt-dlp --dump-json line into an IndexEntry. Missing
// fields map to zero values rather than errors; only malformed JSON fails.
func parseIndexLine(line []byte) (IndexEntry, error) {
	var raw indexLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return IndexEntry{}, fmt.Errorf("parse index line: %w", err)
	}
	return IndexEntry{
		ExternalID:   raw.ID,
		Title:        raw.Title,
		Description:  raw.Description,
		Uploader:     resolveUploader(raw),
		UploadDate:   parseUploadDate(raw.UploadDate),
		Duration:     time.Duration(raw.Duration * float64(time.Second)),
		IsShort:      isShortURL(raw),
		IsLivestream: isLivestream(raw),
	}, nil
}

// resolveUploader prefers the uploader field, falling back to channel.
func resolveUploader(raw indexLine) string {
	if raw.Uploader != "" {
		return raw.Uploader
	}
	return raw.Channel
}

// parseUploadDate converts a YYYYMMDD string to a time, or zero when absent or
// unparseable.
func parseUploadDate(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(uploadDateLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// isShortURL reports whether either URL field marks the item as a Short.
func isShortURL(raw indexLine) bool {
	return strings.Contains(raw.WebpageURL, shortsPathSegment) ||
		strings.Contains(raw.URL, shortsPathSegment)
}

// isLivestream reports whether the item is or was a livestream.
func isLivestream(raw indexLine) bool {
	switch raw.LiveStatus {
	case liveStatusIsLive, liveStatusWasLive, liveStatusPostLive:
		return true
	}
	return raw.WasLive
}

// buildIndexArgs builds the yt-dlp argument list for indexing a collection,
// inserting cookies and the PO-token provider only when supplied.
//
// Without a date window the scan is flat: a fast, shallow listing that carries no
// upload dates. With one, the scan is deep enough to report each item's date and
// is told to stop at the first item older than the window — which turns a whole
// back catalogue into just the handful of recent items actually wanted.
func buildIndexArgs(url string, opts IndexOptions, potProviderURL string, throttle Throttle) []string {
	args := []string{flagDumpJSON, flagIgnoreErrors}
	if opts.DateAfter == "" {
		args = append(args, flagFlatPlaylist)
	} else {
		args = append(args, flagDateAfter, opts.DateAfter, flagBreakOnReject, flagLazyPlaylist)
	}
	args = appendCookies(args, opts.CookiesPath)
	args = appendPOTProvider(args, potProviderURL)
	args = throttle.appendRequestFlags(args)
	return append(args, url)
}

// buildMetadataArgs builds the yt-dlp argument list for fetching one item's full
// metadata. --no-playlist keeps a URL that also belongs to a playlist from
// expanding into the whole collection.
func buildMetadataArgs(url, cookiesPath, potProviderURL string, throttle Throttle) []string {
	args := []string{flagDumpJSON, flagNoPlaylist, flagSkipDownload}
	args = appendCookies(args, cookiesPath)
	args = appendPOTProvider(args, potProviderURL)
	args = throttle.appendRequestFlags(args)
	return append(args, url)
}

// buildDownloadArgs builds the yt-dlp argument list for downloading one item.
func buildDownloadArgs(url string, opts DownloadOptions, potProviderURL string, throttle Throttle) []string {
	args := []string{flagOutput, opts.OutputPath + outputExtTemplate}
	args = appendPaths(args, opts)
	args = appendFormat(args, opts)
	args = appendEmbedFlags(args, opts)
	args = append(args, opts.SponsorBlockArgs...)
	args = appendCookies(args, opts.CookiesPath)
	args = appendPOTProvider(args, potProviderURL)
	if opts.DateAfter != "" {
		args = append(args, flagDateAfter, opts.DateAfter)
	}
	// The throttle goes before the user's own arguments so that anyone who
	// deliberately passes their own --limit-rate or --sleep-interval overrides it;
	// with yt-dlp the last occurrence of a flag wins.
	args = throttle.appendDownloadFlags(args)
	args = append(args, opts.ExtraArgs...)
	args = appendProgressFlags(args)
	return append(args, url)
}

// appendPOTProvider adds the extractor-args that point yt-dlp's bgutil plugin at
// a PO-token provider, only when a base URL is configured. With a provider set,
// most YouTube content downloads without cookies.
func appendPOTProvider(args []string, potProviderURL string) []string {
	if potProviderURL == "" {
		return args
	}
	return append(args, flagExtractorArgs, potProviderArgPrefix+potProviderURL)
}

// appendPaths splits the destination from the scratch space: the finished file
// lands under home, while partial downloads and postprocessor output are written
// to temp and only moved across when complete.
//
// Both are skipped unless HomeDir is set, because yt-dlp ignores --paths whenever
// the output template is an absolute path — the destination has to be given as a
// home directory plus a relative template for any of this to apply.
func appendPaths(args []string, opts DownloadOptions) []string {
	if opts.HomeDir == "" {
		return args
	}
	args = append(args, flagPaths, homePathPrefix+opts.HomeDir)
	if opts.TempDir == "" {
		return args
	}
	return append(args, flagPaths, tempPathPrefix+opts.TempDir)
}

// appendFormat adds the audio-extraction flag or an explicit format selector.
func appendFormat(args []string, opts DownloadOptions) []string {
	if opts.AudioOnly {
		return append(args, flagExtractAudio)
	}
	if opts.Format != "" {
		return append(args, flagFormat, opts.Format)
	}
	return args
}

// appendEmbedFlags adds the metadata, thumbnail, and subtitle embed flags.
func appendEmbedFlags(args []string, opts DownloadOptions) []string {
	if opts.EmbedMetadata {
		args = append(args, flagEmbedMetadata)
	}
	if opts.EmbedThumbnail {
		args = append(args, flagEmbedThumbnail)
	}
	if opts.WriteThumbnail {
		args = append(args, flagWriteThumbnail, flagConvertThumbnails, thumbnailFormat)
	}
	if opts.EmbedSubtitles {
		args = append(args, flagEmbedSubs)
		if len(opts.SubtitleLangs) > 0 {
			args = append(args, flagSubLangs, strings.Join(opts.SubtitleLangs, subLangSeparator))
		}
	}
	return args
}

// appendProgressFlags adds the flags that stream progress and print the final
// file path.
func appendProgressFlags(args []string) []string {
	return append(args,
		flagNewline,
		flagForceProgress,
		flagProgressTemplate, progressTemplateValue,
		flagPrint, afterMovePrintValue,
	)
}

// appendCookies adds the cookies flag only when a path is supplied.
func appendCookies(args []string, cookiesPath string) []string {
	if cookiesPath == "" {
		return args
	}
	return append(args, flagCookies, cookiesPath)
}

// parseProgressPercent extracts the percentage from a "download:NN.N%" line,
// reporting false when the line is not a parseable progress line.
func parseProgressPercent(line string) (float64, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, progressLinePrefix) {
		return 0, false
	}
	value := strings.TrimSpace(strings.TrimPrefix(trimmed, progressLinePrefix))
	value = strings.TrimSuffix(value, "%")
	percent, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	return percent, true
}
