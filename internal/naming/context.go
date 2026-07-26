// Package naming renders user-defined path templates into filesystem paths for
// downloaded media. It is pure: given a template and a context it produces a
// path, with no I/O, so Plex/Jellyfin/Kodi layouts can be unit-tested exactly.
package naming

import (
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
)

// dateLayout is the canonical date format for VarUploadDate (ISO 8601 date).
const dateLayout = "2006-01-02"

// Context supplies the values a template can reference for one media item. It is
// assembled from domain entities at the edge so the naming package never imports
// the database or yt-dlp.
type Context struct {
	SourceName string
	Media      domain.MediaMetadata
	ExternalID string
}

// NewContext builds a rendering context from a source name and a media item.
func NewContext(sourceName string, media domain.Media) Context {
	return Context{
		SourceName: sourceName,
		Media:      media.Metadata,
		ExternalID: media.ExternalID,
	}
}

// values returns the resolved string for every supported variable. Centralizing
// resolution here means adding a variable is a single edit (Open-Closed for the
// template vocabulary).
func (c Context) values() map[Variable]string {
	upload := c.Media.UploadDate
	return map[Variable]string{
		VarSourceName: c.SourceName,
		VarUploader:   c.Media.Uploader,
		VarTitle:      c.Media.Title,
		VarID:         c.ExternalID,
		VarUploadDate: upload.Format(dateLayout),
		VarUploadYear: upload.Format("2006"),
		VarSeason:     "S" + upload.Format("2006"),
		VarEpisode:    upload.Format("20060102"),
	}
}

// zeroDate reports whether a time is the zero value, used to warn when a template
// references a date the provider did not supply.
func zeroDate(t time.Time) bool { return t.IsZero() }
