// Package metadata generates media-server sidecar files (Kodi/Jellyfin .nfo)
// from domain media. It is pure with respect to its inputs: building the XML
// document has no side effects, and only the Writer touches the filesystem.
package metadata

import (
	"encoding/xml"
	"fmt"
	"time"

	"sub_scribe/internal/domain"
)

const (
	// airedDateLayout is the YYYY-MM-DD format Kodi/Jellyfin expect for the
	// <aired> element.
	airedDateLayout = "2006-01-02"

	// uniqueIDType labels the <uniqueid> so media servers know its origin.
	uniqueIDType = "sub_scribe"
)

// episodeDetails is the root <episodedetails> element of a Kodi/Jellyfin
// episode NFO document.
type episodeDetails struct {
	XMLName  xml.Name `xml:"episodedetails"`
	Title    string   `xml:"title"`
	Plot     string   `xml:"plot"`
	Aired    string   `xml:"aired"`
	Studio   string   `xml:"studio"`
	UniqueID uniqueID `xml:"uniqueid"`
	Runtime  int      `xml:"runtime"`
}

// uniqueID is the <uniqueid> element carrying the provider's stable identifier.
type uniqueID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// movieDetails is the root <movie> element of a Kodi-style movie NFO document,
// for a flat library where each video stands alone rather than belonging to a
// season.
type movieDetails struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Plot      string   `xml:"plot"`
	Year      int      `xml:"year"`
	Premiered string   `xml:"premiered"`
	Studio    string   `xml:"studio"`
	UniqueID  uniqueID `xml:"uniqueid"`
	Runtime   int      `xml:"runtime"`
}

// BuildNFO renders media into the NFO document matching the given format. An
// empty or unrecognized format falls back to the Jellyfin layout, since the
// Kodi/Jellyfin NFO is the widely-read default.
func BuildNFO(media domain.Media, sourceName string, format domain.MetadataFormat) ([]byte, error) {
	if format == domain.MetadataMovie {
		return BuildMovieNFO(media, sourceName)
	}
	return BuildEpisodeNFO(media, sourceName)
}

// BuildEpisodeNFO renders media into a Kodi/Jellyfin-compatible
// <episodedetails> XML document. sourceName becomes the <studio>. The returned
// bytes carry the XML header and are safe to write directly to a .nfo file.
func BuildEpisodeNFO(media domain.Media, sourceName string) ([]byte, error) {
	details := newEpisodeDetails(media, sourceName)
	return marshalNFO(details, "episode")
}

// BuildMovieNFO renders media into a Plex-compatible <movie> XML document.
func BuildMovieNFO(media domain.Media, sourceName string) ([]byte, error) {
	return marshalNFO(newMovieDetails(media, sourceName), "movie")
}

// marshalNFO renders an NFO element to bytes with the XML header prepended.
func marshalNFO(details any, kind string) ([]byte, error) {
	body, err := xml.MarshalIndent(details, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal %s nfo: %w", kind, err)
	}
	return append([]byte(xml.Header), body...), nil
}

// newMovieDetails maps domain media onto the movie NFO element structure.
func newMovieDetails(media domain.Media, sourceName string) movieDetails {
	meta := media.Metadata
	return movieDetails{
		Title:     meta.Title,
		Plot:      meta.Description,
		Year:      meta.UploadDate.Year(),
		Premiered: meta.UploadDate.Format(airedDateLayout),
		Studio:    sourceName,
		UniqueID:  uniqueID{Type: uniqueIDType, Value: media.ExternalID},
		Runtime:   runtimeMinutes(meta.Duration),
	}
}

// newEpisodeDetails maps domain media onto the NFO element structure.
func newEpisodeDetails(media domain.Media, sourceName string) episodeDetails {
	meta := media.Metadata
	return episodeDetails{
		Title:    meta.Title,
		Plot:     meta.Description,
		Aired:    meta.UploadDate.Format(airedDateLayout),
		Studio:   sourceName,
		UniqueID: uniqueID{Type: uniqueIDType, Value: media.ExternalID},
		Runtime:  runtimeMinutes(meta.Duration),
	}
}

// runtimeMinutes converts a duration to whole minutes, truncating any
// remaining seconds, matching the integer <runtime> media servers expect.
func runtimeMinutes(d time.Duration) int {
	return int(d / time.Minute)
}
