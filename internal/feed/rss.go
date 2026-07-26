// Package feed generates RSS 2.0 podcast feeds from a source's downloaded media.
// BuildRSS renders the XML; Writer persists it to disk. The package depends only
// on the domain value types, keeping feed rendering pure and testable.
package feed

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"sub_scribe/internal/domain"
)

const (
	// itunesNamespace is the iTunes podcast XML namespace declared on the channel.
	itunesNamespace = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	// mediaPathPrefix is the relative directory enclosure URLs are rooted at.
	mediaPathPrefix = "media/"

	mimeVideoMP4 = "video/mp4"
	mimeAudioM4A = "audio/mp4"
	mimeAudioMP3 = "audio/mpeg"
	mimeDefault  = "application/octet-stream"

	// pubDateLayout is RFC1123Z, the RSS-standard date format.
	pubDateLayout = "Mon, 02 Jan 2006 15:04:05 -0700"
)

// rss is the document root: <rss version="2.0" xmlns:itunes="...">.
type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Itunes  string   `xml:"xmlns:itunes,attr"`
	Channel channel  `xml:"channel"`
}

// channel is the feed's single <channel> element.
type channel struct {
	Title string `xml:"title"`
	Items []item `xml:"item"`
}

// item is one <item> element for a downloaded media file.
type item struct {
	Title     string    `xml:"title"`
	GUID      guid      `xml:"guid"`
	PubDate   string    `xml:"pubDate"`
	Enclosure enclosure `xml:"enclosure"`
}

// guid is the item's globally unique id; isPermaLink=false marks it opaque.
type guid struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// enclosure references the downloadable media file.
type enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// BuildRSS renders an RSS 2.0 podcast feed for a source. Only items whose Status
// is domain.MediaDownloaded are included, and they are ordered newest first by
// upload date. The returned bytes are a complete XML document.
func BuildRSS(source domain.Source, items []domain.Media) ([]byte, error) {
	downloaded := filterDownloaded(items)
	sort.SliceStable(downloaded, func(i, j int) bool {
		return downloaded[i].Metadata.UploadDate.After(downloaded[j].Metadata.UploadDate)
	})

	doc := rss{
		Version: "2.0",
		Itunes:  itunesNamespace,
		Channel: channel{Title: source.Name, Items: make([]item, 0, len(downloaded))},
	}
	for _, m := range downloaded {
		doc.Channel.Items = append(doc.Channel.Items, toItem(m))
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal rss: %w", err)
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// filterDownloaded returns only the items present on disk.
func filterDownloaded(items []domain.Media) []domain.Media {
	out := make([]domain.Media, 0, len(items))
	for _, m := range items {
		if m.Status == domain.MediaDownloaded {
			out = append(out, m)
		}
	}
	return out
}

// toItem maps a downloaded media record onto an RSS <item>.
func toItem(m domain.Media) item {
	return item{
		Title:   m.Metadata.Title,
		GUID:    guid{IsPermaLink: false, Value: m.ExternalID},
		PubDate: m.Metadata.UploadDate.Format(pubDateLayout),
		Enclosure: enclosure{
			URL:    mediaPathPrefix + m.ExternalID,
			Length: m.FileSize,
			Type:   mimeForPath(m.FilePath),
		},
	}
}

// mimeForPath infers an enclosure MIME type from a file's extension, falling back
// to application/octet-stream for unknown extensions.
func mimeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		return mimeVideoMP4
	case ".m4a":
		return mimeAudioM4A
	case ".mp3":
		return mimeAudioMP3
	default:
		return mimeDefault
	}
}
