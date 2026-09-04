package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

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

	// FeedToken is the capability secret in the source's podcast feed URL
	// (/feeds/{id}?t=token). Podcast apps cannot log in through a browser SSO
	// flow, so the token in the URL is what authorizes them when the UI is
	// otherwise locked. Assigned at creation and never rotated by edits.
	FeedToken string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// feedTokenBytes sizes the feed token: 16 random bytes (128 bits) rendered as
// 32 hex characters — unguessable, yet short enough to keep feed URLs tidy.
const feedTokenBytes = 16

// NewFeedToken returns a fresh feed capability token from crypto/rand.
func NewFeedToken() string {
	buf := make([]byte, feedTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing means the OS entropy source is broken; there is no
		// safe fallback for a secret, so this is one of the rare justified panics.
		panic(fmt.Sprintf("domain: generating feed token: %v", err))
	}
	return hex.EncodeToString(buf)
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
