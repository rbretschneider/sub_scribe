package domain

import "time"

// Source is a remote collection (channel or playlist) that sub_scribe tracks and
// keeps downloaded. It carries the rules that decide which of the collection's
// media are kept: date cutoffs, title filters, Shorts/livestream handling, and
// cookie behavior. A Source references a MediaProfile for the how/where of
// downloading, keeping "what to track" and "how to store it" independent.
type Source struct {
	ID             int64
	Name           string
	URL            string
	CollectionType CollectionType
	MediaProfileID int64

	Enabled bool

	// IndexFrequency is how often the collection is re-scanned for new media.
	IndexFrequency time.Duration
	LastIndexedAt  *time.Time

	CookieBehavior CookieBehavior

	// DownloadCutoff, when set, excludes media uploaded before this fixed date.
	DownloadCutoff *time.Time
	// CutoffWindow, when non-zero, excludes media older than this rolling window
	// (e.g. "the last 365 days"), recomputed relative to now at each scan. It
	// takes precedence over DownloadCutoff.
	CutoffWindow time.Duration
	// TitleFilterPattern, when non-empty, is a regular expression a media title
	// must match to be downloaded.
	TitleFilterPattern string

	ShortsRule      InclusionRule
	LivestreamsRule InclusionRule

	// RetentionAfter, when non-zero, deletes downloaded media older than this age.
	RetentionAfter time.Duration

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveCutoff resolves the source's download cutoff as of now: the rolling
// window (now minus CutoffWindow) when one is set, otherwise the fixed
// DownloadCutoff, or nil when neither is configured. This is what filtering
// actually compares an item's upload date against.
func (s Source) EffectiveCutoff(now time.Time) *time.Time {
	if s.CutoffWindow > 0 {
		cutoff := now.Add(-s.CutoffWindow)
		return &cutoff
	}
	return s.DownloadCutoff
}

// IsDueForIndex reports whether the source should be re-scanned as of now. A
// source that has never been indexed is always due; otherwise it is due once
// IndexFrequency has elapsed since the last index.
func (s Source) IsDueForIndex(now time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.LastIndexedAt == nil {
		return true
	}
	return now.Sub(*s.LastIndexedAt) >= s.IndexFrequency
}
