package metadata

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

// sampleMedia returns a Media value with fully populated metadata for tests.
func sampleMedia() domain.Media {
	return domain.Media{
		ExternalID: "abc123",
		Metadata: domain.MediaMetadata{
			Title:       "Episode One",
			Description: "A plot description.",
			UploadDate:  time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC),
			Duration:    12*time.Minute + 45*time.Second,
		},
	}
}

// parsedNFO unmarshals NFO bytes back into the episode structure for assertions.
func parsedNFO(t *testing.T, body []byte) episodeDetails {
	t.Helper()
	var got episodeDetails
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal nfo: %v", err)
	}
	return got
}

func TestBuildEpisodeNFO(t *testing.T) {
	tests := []struct {
		name       string
		media      domain.Media
		sourceName string
		want       episodeDetails
	}{
		{
			name:       "fully populated",
			media:      sampleMedia(),
			sourceName: "My Channel",
			want: episodeDetails{
				Title:    "Episode One",
				Plot:     "A plot description.",
				Aired:    "2026-03-05",
				Studio:   "My Channel",
				UniqueID: uniqueID{Type: uniqueIDType, Value: "abc123"},
				Runtime:  12,
			},
		},
		{
			name: "sub-minute duration truncates to zero",
			media: domain.Media{
				ExternalID: "z9",
				Metadata: domain.MediaMetadata{
					Title:      "Short",
					UploadDate: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
					Duration:   30 * time.Second,
				},
			},
			sourceName: "Src",
			want: episodeDetails{
				Title:    "Short",
				Aired:    "2025-12-31",
				Studio:   "Src",
				UniqueID: uniqueID{Type: uniqueIDType, Value: "z9"},
				Runtime:  0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := BuildEpisodeNFO(tt.media, tt.sourceName)
			if err != nil {
				t.Fatalf("BuildEpisodeNFO: %v", err)
			}
			assertHasXMLHeader(t, body)
			got := parsedNFO(t, body)
			got.XMLName = xml.Name{}
			if got != tt.want {
				t.Errorf("nfo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func assertHasXMLHeader(t *testing.T, body []byte) {
	t.Helper()
	if !strings.HasPrefix(string(body), strings.TrimSpace(xml.Header)) {
		t.Errorf("nfo missing xml header, got prefix %q", string(body[:min(len(body), 40)]))
	}
}

func TestBuildEpisodeNFORootElement(t *testing.T) {
	body, err := BuildEpisodeNFO(sampleMedia(), "Src")
	if err != nil {
		t.Fatalf("BuildEpisodeNFO: %v", err)
	}
	if !strings.Contains(string(body), "<episodedetails>") {
		t.Errorf("expected <episodedetails> root, got:\n%s", body)
	}
}

func TestBuildMovieNFO(t *testing.T) {
	body, err := BuildMovieNFO(sampleMedia(), "My Channel")
	if err != nil {
		t.Fatalf("BuildMovieNFO: %v", err)
	}
	assertHasXMLHeader(t, body)

	var got movieDetails
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal movie nfo: %v", err)
	}
	got.XMLName = xml.Name{}
	want := movieDetails{
		Title:     "Episode One",
		Plot:      "A plot description.",
		Year:      2026,
		Premiered: "2026-03-05",
		Studio:    "My Channel",
		UniqueID:  uniqueID{Type: uniqueIDType, Value: "abc123"},
		Runtime:   12,
	}
	if got != want {
		t.Errorf("movie nfo = %+v, want %+v", got, want)
	}
}

func TestBuildNFODispatchesOnFormat(t *testing.T) {
	tests := []struct {
		name     string
		format   domain.MetadataFormat
		wantRoot string
	}{
		{name: "plex -> movie", format: domain.MetadataMovie, wantRoot: "<movie>"},
		{name: "jellyfin -> episode", format: domain.MetadataEpisode, wantRoot: "<episodedetails>"},
		{name: "empty falls back to episode", format: "", wantRoot: "<episodedetails>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := BuildNFO(sampleMedia(), "Src", tt.format)
			if err != nil {
				t.Fatalf("BuildNFO: %v", err)
			}
			if !strings.Contains(string(body), tt.wantRoot) {
				t.Errorf("format %q: expected root %s, got:\n%s", tt.format, tt.wantRoot, body)
			}
		})
	}
}

func TestWriteForPlexWritesMovieNFO(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(mediaPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	w := NewWriter()
	if err := w.WriteFor(context.Background(), mediaPath, sampleMedia(), "Chan", domain.MetadataMovie); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "clip.nfo"))
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}
	if !strings.Contains(string(body), "<movie>") {
		t.Errorf("expected Plex <movie> nfo, got:\n%s", body)
	}
}

func TestWriteForCreatesSidecar(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "video.mkv")
	if err := os.WriteFile(mediaPath, []byte("fake media"), 0o644); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	w := NewWriter()
	if err := w.WriteFor(context.Background(), mediaPath, sampleMedia(), "My Channel", domain.MetadataEpisode); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}

	nfoPath := filepath.Join(dir, "video.nfo")
	body, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}
	got := parsedNFO(t, body)
	if got.Title != "Episode One" {
		t.Errorf("nfo title = %q, want %q", got.Title, "Episode One")
	}
	if got.UniqueID.Value != "abc123" {
		t.Errorf("nfo uniqueid = %q, want %q", got.UniqueID.Value, "abc123")
	}
}

func TestWriteForCancelledContext(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "video.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := NewWriter()
	if err := w.WriteFor(ctx, mediaPath, sampleMedia(), "Src", domain.MetadataMovie); err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "video.nfo")); !os.IsNotExist(err) {
		t.Errorf("expected no nfo written on cancelled context, stat err = %v", err)
	}
}

func TestNFOPathFor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "mkv", in: "/a/b/video.mkv", want: "/a/b/video.nfo"},
		{name: "mp3", in: "/a/b/audio.mp3", want: "/a/b/audio.nfo"},
		{name: "no extension", in: "/a/b/plain", want: "/a/b/plain.nfo"},
		{name: "dotted name", in: "/a/b/my.video.webm", want: "/a/b/my.video.nfo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nfoPathFor(tt.in); got != tt.want {
				t.Errorf("nfoPathFor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
