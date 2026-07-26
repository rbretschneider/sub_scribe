package feed

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

func mediaAt(extID, title, path string, size int64, status domain.MediaStatus, up time.Time) domain.Media {
	return domain.Media{
		ExternalID: extID,
		FilePath:   path,
		FileSize:   size,
		Status:     status,
		Metadata:   domain.MediaMetadata{Title: title, UploadDate: up},
	}
}

// parsed mirrors the on-wire structure for assertions.
type parsed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title   string `xml:"title"`
			PubDate string `xml:"pubDate"`
			GUID    struct {
				IsPermaLink string `xml:"isPermaLink,attr"`
				Value       string `xml:",chardata"`
			} `xml:"guid"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
				Type   string `xml:"type,attr"`
			} `xml:"enclosure"`
		} `xml:"item"`
	} `xml:"channel"`
}

func TestBuildRSS(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2021, 6, 15, 12, 0, 0, 0, time.UTC)
	newest := time.Date(2023, 3, 10, 8, 0, 0, 0, time.UTC)

	src := domain.Source{ID: 7, Name: "My Channel"}
	items := []domain.Media{
		mediaAt("a1", "Oldest", "/data/a1.mp4", 100, domain.MediaDownloaded, old),
		mediaAt("p1", "Pending", "/data/p1.mp4", 5, domain.MediaPending, mid),
		mediaAt("n1", "Newest", "/data/n1.m4a", 200, domain.MediaDownloaded, newest),
		mediaAt("s1", "Skipped", "/data/s1.mp3", 9, domain.MediaSkipped, mid),
		mediaAt("m1", "Middle", "/data/m1.mp3", 300, domain.MediaDownloaded, mid),
		mediaAt("f1", "Failed", "/data/f1.mp4", 1, domain.MediaFailed, newest),
	}

	out, err := BuildRSS(src, items)
	if err != nil {
		t.Fatalf("BuildRSS: %v", err)
	}

	var p parsed
	if err := xml.Unmarshal(out, &p); err != nil {
		t.Fatalf("output is not valid XML: %v", err)
	}

	if p.Version != "2.0" {
		t.Errorf("version = %q, want 2.0", p.Version)
	}
	if !strings.Contains(string(out), itunesNamespace) {
		t.Errorf("itunes namespace missing from output")
	}
	if p.Channel.Title != "My Channel" {
		t.Errorf("channel title = %q, want My Channel", p.Channel.Title)
	}

	// Only downloaded items, newest first.
	if len(p.Channel.Items) != 3 {
		t.Fatalf("got %d items, want 3 (downloaded only)", len(p.Channel.Items))
	}
	wantOrder := []string{"n1", "m1", "a1"}
	for i, want := range wantOrder {
		if got := p.Channel.Items[i].GUID.Value; got != want {
			t.Errorf("item[%d] guid = %q, want %q", i, got, want)
		}
	}

	// Non-downloaded ids must be absent.
	for _, it := range p.Channel.Items {
		switch it.GUID.Value {
		case "p1", "s1", "f1":
			t.Errorf("non-downloaded item %q leaked into feed", it.GUID.Value)
		}
	}

	first := p.Channel.Items[0]
	if first.GUID.IsPermaLink != "false" {
		t.Errorf("guid isPermaLink = %q, want false", first.GUID.IsPermaLink)
	}
	if first.Title != "Newest" {
		t.Errorf("first item title = %q, want Newest", first.Title)
	}
	if first.Enclosure.URL != "media/n1" {
		t.Errorf("enclosure url = %q, want media/n1", first.Enclosure.URL)
	}
	if first.Enclosure.Length != 200 {
		t.Errorf("enclosure length = %d, want 200", first.Enclosure.Length)
	}
	if first.Enclosure.Type != mimeAudioM4A {
		t.Errorf("enclosure type = %q, want %q", first.Enclosure.Type, mimeAudioM4A)
	}
	if want := newest.Format(pubDateLayout); first.PubDate != want {
		t.Errorf("pubDate = %q, want %q", first.PubDate, want)
	}
}

func TestBuildRSSEmpty(t *testing.T) {
	out, err := BuildRSS(domain.Source{Name: "Empty"}, nil)
	if err != nil {
		t.Fatalf("BuildRSS: %v", err)
	}
	var p parsed
	if err := xml.Unmarshal(out, &p); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(p.Channel.Items) != 0 {
		t.Errorf("got %d items, want 0", len(p.Channel.Items))
	}
}

func TestMimeForPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/x/video.mp4", mimeVideoMP4},
		{"/x/AUDIO.M4A", mimeAudioM4A},
		{"/x/song.mp3", mimeAudioMP3},
		{"/x/file.webm", mimeDefault},
		{"/x/noext", mimeDefault},
		{"", mimeDefault},
	}
	for _, tt := range tests {
		if got := mimeForPath(tt.path); got != tt.want {
			t.Errorf("mimeForPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestWriteFeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "feeds")
	w := NewWriter(dir)
	src := domain.Source{ID: 42, Name: "Writer Test"}
	items := []domain.Media{
		mediaAt("z1", "One", "/d/z1.mp4", 10, domain.MediaDownloaded, time.Now()),
	}

	if err := w.WriteFeed(context.Background(), src, items); err != nil {
		t.Fatalf("WriteFeed: %v", err)
	}

	path := filepath.Join(dir, "42.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected feed file at %s: %v", path, err)
	}
	var p parsed
	if err := xml.Unmarshal(data, &p); err != nil {
		t.Fatalf("written feed is not valid XML: %v", err)
	}
	if p.Channel.Title != "Writer Test" {
		t.Errorf("title = %q, want Writer Test", p.Channel.Title)
	}
	if len(p.Channel.Items) != 1 || p.Channel.Items[0].GUID.Value != "z1" {
		t.Errorf("unexpected items: %+v", p.Channel.Items)
	}
}

func TestWriteFeedCanceledContext(t *testing.T) {
	w := NewWriter(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.WriteFeed(ctx, domain.Source{ID: 1}, nil); err == nil {
		t.Error("expected error from canceled context")
	}
}
