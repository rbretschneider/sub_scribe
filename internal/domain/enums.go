// Package domain holds sub_scribe's core entities and value types. It models the
// media-archiving concepts (sources, media, profiles, tasks) and their rules,
// and deliberately imports no infrastructure (no database, HTTP, or yt-dlp) so
// the domain stays testable and portable.
package domain

// CollectionType distinguishes the kind of remote collection a Source tracks.
// It governs how yt-dlp is asked to enumerate the collection's media.
type CollectionType string

const (
	// CollectionChannel is an entire uploader/channel.
	CollectionChannel CollectionType = "channel"
	// CollectionPlaylist is a single ordered playlist.
	CollectionPlaylist CollectionType = "playlist"
)

// IsValid reports whether the collection type is a recognized value.
func (c CollectionType) IsValid() bool {
	return c == CollectionChannel || c == CollectionPlaylist
}

// MediaKind selects whether a Source downloads full video or extracts audio only.
type MediaKind string

const (
	// MediaVideo downloads video with audio.
	MediaVideo MediaKind = "video"
	// MediaAudio extracts audio only (e.g. for podcast feeds).
	MediaAudio MediaKind = "audio"
)

// IsValid reports whether the media kind is a recognized value.
func (m MediaKind) IsValid() bool {
	return m == MediaVideo || m == MediaAudio
}

// CookieBehavior controls when a Source hands YouTube cookies to yt-dlp. Cookies
// let yt-dlp reach age-restricted and private content but carry ban risk, so the
// default minimizes their use.
type CookieBehavior string

const (
	// CookieDisabled never sends cookies.
	CookieDisabled CookieBehavior = "disabled"
	// CookieWhenNeeded sends cookies only after an operation fails in a way that
	// suggests authentication is required (the recommended default).
	CookieWhenNeeded CookieBehavior = "when_needed"
	// CookieAllOperations sends cookies for every yt-dlp invocation.
	CookieAllOperations CookieBehavior = "all_operations"
)

// IsValid reports whether the cookie behavior is a recognized value.
func (c CookieBehavior) IsValid() bool {
	switch c {
	case CookieDisabled, CookieWhenNeeded, CookieAllOperations:
		return true
	default:
		return false
	}
}

// InclusionRule decides whether a category of media (Shorts, livestreams) is
// downloaded, ignored, or downloaded only when it already exists locally.
type InclusionRule string

const (
	// InclusionInclude downloads media in the category.
	InclusionInclude InclusionRule = "include"
	// InclusionExclude skips media in the category entirely.
	InclusionExclude InclusionRule = "exclude"
)

// IsValid reports whether the inclusion rule is a recognized value.
func (i InclusionRule) IsValid() bool {
	return i == InclusionInclude || i == InclusionExclude
}

// MediaStatus is the lifecycle state of a single downloadable item.
type MediaStatus string

const (
	// MediaPending has been indexed but not yet downloaded.
	MediaPending MediaStatus = "pending"
	// MediaDownloading is actively being fetched by yt-dlp.
	MediaDownloading MediaStatus = "downloading"
	// MediaDownloaded is present on disk.
	MediaDownloaded MediaStatus = "downloaded"
	// MediaFailed errored during download and may be retried.
	MediaFailed MediaStatus = "failed"
	// MediaSkipped was excluded by a Source rule (filter, cutoff, Shorts, etc.).
	MediaSkipped MediaStatus = "skipped"
	// MediaUnavailable is refused by the provider — members-only, private,
	// removed, or region-locked. Distinct from MediaFailed because retrying
	// cannot help, and distinct from MediaSkipped because the user did not ask to
	// exclude it and probably wants to know it exists.
	MediaUnavailable MediaStatus = "unavailable"
)

// IsValid reports whether the media status is a recognized value.
func (m MediaStatus) IsValid() bool {
	switch m {
	case MediaPending, MediaDownloading, MediaDownloaded, MediaFailed,
		MediaSkipped, MediaUnavailable:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the status represents a settled outcome that the
// scheduler will not act on again without an explicit retry.
func (m MediaStatus) IsTerminal() bool {
	return m == MediaDownloaded || m == MediaSkipped || m == MediaUnavailable
}
