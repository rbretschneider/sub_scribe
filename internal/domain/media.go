package domain

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Media is a single downloadable item (one video or audio track) discovered
// within a Source. Its lifecycle is tracked by Status so the scheduler can pick
// up pending work and retry failures without re-indexing.
type Media struct {
	ID       int64
	SourceID int64

	// ExternalID is the provider's stable identifier (the YouTube video id),
	// used to deduplicate across re-indexes.
	ExternalID string

	Metadata MediaMetadata

	Status    MediaStatus
	FilePath  string // absolute path once downloaded; empty otherwise
	FileSize  int64
	Attempts  int
	LastError string

	DownloadedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// youtubeWatchBase is where a video's external id resolves to a watch page.
// YouTube is currently the only supported provider.
const youtubeWatchBase = "https://www.youtube.com/watch?v="

// WatchURL returns the provider watch page for a video's external id, or empty
// when the id is unknown. It is the single place a watch link is built, so the
// UI, the downloader, and metadata files can never disagree about it.
func WatchURL(externalID string) string {
	if externalID == "" {
		return ""
	}
	return youtubeWatchBase + externalID
}

// WatchURL returns the provider watch page for this item, or empty when its
// external id is unknown.
func (m Media) WatchURL() string {
	return WatchURL(m.ExternalID)
}

// watchIDPattern matches a YouTube video id: exactly eleven URL-safe base64
// characters.
var watchIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// videoPathPrefixes are the URL path shapes that carry the video id as the
// following segment.
var videoPathPrefixes = []string{"/shorts/", "/live/", "/embed/"}

// ParseWatchID extracts the video id from any of the URL shapes YouTube hands
// out for a single video — watch?v=, youtu.be short links, Shorts, live, and
// embed paths, or a bare id — reporting false for anything else, such as a
// channel or playlist URL.
func ParseWatchID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if watchIDPattern.MatchString(raw) {
		return raw, true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}

	if strings.EqualFold(parsed.Hostname(), "youtu.be") {
		return validWatchID(strings.TrimPrefix(parsed.Path, "/"))
	}
	if id := parsed.Query().Get("v"); id != "" {
		return validWatchID(id)
	}
	for _, prefix := range videoPathPrefixes {
		if rest, ok := strings.CutPrefix(parsed.Path, prefix); ok {
			id, _, _ := strings.Cut(rest, "/")
			return validWatchID(id)
		}
	}
	return "", false
}

// validWatchID returns the candidate when it is a well-formed video id.
func validWatchID(candidate string) (string, bool) {
	if !watchIDPattern.MatchString(candidate) {
		return "", false
	}
	return candidate, true
}

// MediaMetadata is the descriptive information yt-dlp reports for an item. It is
// a value object: pure data used by naming, filtering, and metadata-file
// generation, with no identity of its own.
type MediaMetadata struct {
	Title        string
	Description  string
	Uploader     string
	UploadDate   time.Time
	Duration     time.Duration
	IsShort      bool
	IsLivestream bool
}

// PassesFilters reports whether this item's metadata satisfies the given
// source's inclusion rules: upload cutoff, title filter, and Shorts/livestream
// handling. The title matcher is injected so the domain stays free of the regexp
// engine's construction concerns and the caller can pre-compile patterns.
func (m MediaMetadata) PassesFilters(src Source, titleMatches func(string) bool) bool {
	// Only apply the cutoff when the upload date is actually known. Fast indexing
	// (yt-dlp --flat-playlist) does not report dates, so filtering on a zero date
	// here would reject everything; the cutoff is enforced at download time
	// instead, where the real date is available.
	if src.DownloadCutoff != nil && !m.UploadDate.IsZero() && m.UploadDate.Before(*src.DownloadCutoff) {
		return false
	}
	if m.IsShort && src.ShortsRule == InclusionExclude {
		return false
	}
	if m.IsLivestream && src.LivestreamsRule == InclusionExclude {
		return false
	}
	if titleMatches != nil && !titleMatches(m.Title) {
		return false
	}
	return true
}
