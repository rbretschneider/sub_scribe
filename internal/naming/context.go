// Package naming renders user-defined path templates into filesystem paths for
// downloaded media. It is pure: given a template and a context it produces a
// path, with no I/O, so Plex/Jellyfin/Kodi layouts can be unit-tested exactly.
package naming

import (
	"fmt"
	"time"

	"sub_scribe/internal/domain"
)

// Variable is a template placeholder name usable inside {{ }}. Using named
// constants (not bare strings scattered through the code) keeps the supported
// vocabulary in one place and lets the renderer validate templates.
type Variable string

const (
	VarSourceName Variable = "source_name"
	VarUploader   Variable = "uploader"
	VarTitle      Variable = "title"
	VarID         Variable = "id"
	VarUploadDate Variable = "upload_date"
	VarUploadYear Variable = "upload_year"
	VarSeason     Variable = "season"  // upload year, formatted as Plex season "SYYYY"
	VarEpisode    Variable = "episode" // upload date, formatted as "YYYYMMDD"
	// VarUploadMMDD is the upload month and day as "MMDD", the episode number in
	// a season-per-year layout.
	VarUploadMMDD Variable = "upload_mmdd"
	// VarSeasonEpisode is the whole "s2026e072401" token: season by year, episode
	// by upload date, plus a two-digit index that separates videos published on
	// the same day.
	//
	// This is the shape media servers actually parse. A plain date in the filename
	// leaves Plex matching against its TV database, failing, and inventing titles
	// like "Episode 04-22"; a season/episode token makes it read the title that
	// follows instead.
	VarSeasonEpisode Variable = "season_episode"
)

// sameDayIndexWidth pads the same-day index to a fixed width so episode numbers
// sort correctly and stay a predictable length.
const sameDayIndexWidth = 2

// dateLayout is the canonical date format for VarUploadDate (ISO 8601 date).
const dateLayout = "2006-01-02"

// Context supplies the values a template can reference for one media item. It is
// assembled from domain entities at the edge so the naming package never imports
// the database or yt-dlp.
type Context struct {
	SourceName string
	Media      domain.MediaMetadata
	ExternalID string
	// SameDayIndex distinguishes videos published on the same day, which would
	// otherwise share an episode number and be treated as one episode. It is
	// 1-based; zero is treated as the first.
	SameDayIndex int
}

// NewContext builds a rendering context from a source name and a media item.
func NewContext(sourceName string, media domain.Media) Context {
	return Context{
		SourceName: sourceName,
		Media:      media.Metadata,
		ExternalID: media.ExternalID,
	}
}

// WithSameDayIndex returns a copy of the context ranked among the videos sharing
// its upload date.
func (c Context) WithSameDayIndex(index int) Context {
	c.SameDayIndex = index
	return c
}

// values returns the resolved string for every supported variable. Centralizing
// resolution here means adding a variable is a single edit (Open-Closed for the
// template vocabulary).
func (c Context) values() map[Variable]string {
	upload := c.Media.UploadDate
	return map[Variable]string{
		VarSourceName:    c.SourceName,
		VarUploader:      c.Media.Uploader,
		VarTitle:         c.Media.Title,
		VarID:            c.ExternalID,
		VarUploadDate:    upload.Format(dateLayout),
		VarUploadYear:    upload.Format("2006"),
		VarSeason:        "S" + upload.Format("2006"),
		VarEpisode:       upload.Format("20060102"),
		VarUploadMMDD:    upload.Format("0102"),
		VarSeasonEpisode: c.seasonEpisode(upload),
	}
}

// seasonEpisode renders the "s2026e072401" token: the year as the season, the
// month and day as the episode, and the same-day index appended so two videos
// published on one day do not collide on a single episode number.
func (c Context) seasonEpisode(upload time.Time) string {
	index := c.SameDayIndex
	if index < 1 {
		index = 1
	}
	return fmt.Sprintf("s%se%s%0*d",
		upload.Format("2006"), upload.Format("0102"), sameDayIndexWidth, index)
}

// zeroDate reports whether a time is the zero value, used to warn when a template
// references a date the provider did not supply.
func zeroDate(t time.Time) bool { return t.IsZero() }
