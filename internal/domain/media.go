package domain

import "time"

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
