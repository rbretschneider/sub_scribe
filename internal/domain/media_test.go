package domain

import (
	"strings"
	"testing"
	"time"
)

// allowAll is the no-op title matcher used when a test does not exercise title
// filtering, keeping each case focused on a single rule.
func allowAll(string) bool { return true }

func TestWatchURLBuildsTheWatchPageOrNothing(t *testing.T) {
	tests := []struct {
		name       string
		externalID string
		want       string
	}{
		{name: "a known id resolves to its watch page", externalID: "abc123", want: "https://www.youtube.com/watch?v=abc123"},
		{name: "an unknown id yields no link", externalID: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WatchURL(tt.externalID); got != tt.want {
				t.Errorf("WatchURL(%q) = %q, want %q", tt.externalID, got, tt.want)
			}
			media := Media{ExternalID: tt.externalID}
			if got := media.WatchURL(); got != tt.want {
				t.Errorf("Media.WatchURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseWatchIDReadsEveryLinkShapeAndRejectsCollections(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wantID string
		wantOK bool
	}{
		{name: "watch link", raw: "https://www.youtube.com/watch?v=gCZOjDar1tU", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "watch link with extra params", raw: "https://www.youtube.com/watch?v=gCZOjDar1tU&t=42s&list=PLx", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "short link", raw: "https://youtu.be/gCZOjDar1tU?si=abc", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "shorts link", raw: "https://www.youtube.com/shorts/gCZOjDar1tU", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "live link", raw: "https://www.youtube.com/live/gCZOjDar1tU", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "embed link", raw: "https://www.youtube.com/embed/gCZOjDar1tU", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "bare id", raw: "gCZOjDar1tU", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "surrounding whitespace", raw: "  https://youtu.be/gCZOjDar1tU  ", wantID: "gCZOjDar1tU", wantOK: true},
		{name: "a channel is not a video", raw: "https://www.youtube.com/@Channel5YouTube", wantOK: false},
		{name: "a playlist is not a video", raw: "https://www.youtube.com/playlist?list=PLx", wantOK: false},
		{name: "not a url at all", raw: "watch this!", wantOK: false},
		{name: "empty", raw: "", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, ok := ParseWatchID(test.raw)
			if ok != test.wantOK || id != test.wantID {
				t.Errorf("ParseWatchID(%q) = %q, %v; want %q, %v",
					test.raw, id, ok, test.wantID, test.wantOK)
			}
		})
	}
}

func TestMediaMetadataPassesFilters(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	beforeCutoff := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	afterCutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		meta       MediaMetadata
		source     Source
		titleMatch func(string) bool
		wantPass   bool
	}{
		{
			name:       "no rules passes",
			meta:       MediaMetadata{UploadDate: afterCutoff},
			source:     Source{},
			titleMatch: allowAll,
			wantPass:   true,
		},
		{
			name:       "before cutoff is excluded",
			meta:       MediaMetadata{UploadDate: beforeCutoff},
			source:     Source{DownloadCutoff: &cutoff},
			titleMatch: allowAll,
			wantPass:   false,
		},
		{
			name:       "after cutoff is included",
			meta:       MediaMetadata{UploadDate: afterCutoff},
			source:     Source{DownloadCutoff: &cutoff},
			titleMatch: allowAll,
			wantPass:   true,
		},
		{
			// Fast indexing lacks dates; an unknown date must not be filtered out
			// by the cutoff (it is enforced later, at download time).
			name:       "unknown upload date is not filtered by cutoff",
			meta:       MediaMetadata{UploadDate: time.Time{}},
			source:     Source{DownloadCutoff: &cutoff},
			titleMatch: allowAll,
			wantPass:   true,
		},
		{
			name:       "short excluded when rule excludes shorts",
			meta:       MediaMetadata{UploadDate: afterCutoff, IsShort: true},
			source:     Source{ShortsRule: InclusionExclude},
			titleMatch: allowAll,
			wantPass:   false,
		},
		{
			name:       "short included when rule includes shorts",
			meta:       MediaMetadata{UploadDate: afterCutoff, IsShort: true},
			source:     Source{ShortsRule: InclusionInclude},
			titleMatch: allowAll,
			wantPass:   true,
		},
		{
			name:       "livestream excluded when rule excludes livestreams",
			meta:       MediaMetadata{UploadDate: afterCutoff, IsLivestream: true},
			source:     Source{LivestreamsRule: InclusionExclude},
			titleMatch: allowAll,
			wantPass:   false,
		},
		{
			name:       "title not matching filter is excluded",
			meta:       MediaMetadata{Title: "Vlog #3", UploadDate: afterCutoff},
			source:     Source{TitleFilterPattern: "Tutorial"},
			titleMatch: func(title string) bool { return strings.Contains(title, "Tutorial") },
			wantPass:   false,
		},
		{
			name:       "title matching filter is included",
			meta:       MediaMetadata{Title: "Go Tutorial 1", UploadDate: afterCutoff},
			source:     Source{TitleFilterPattern: "Tutorial"},
			titleMatch: func(title string) bool { return strings.Contains(title, "Tutorial") },
			wantPass:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.meta.PassesFilters(tc.source, tc.titleMatch); got != tc.wantPass {
				t.Errorf("PassesFilters() = %v, want %v", got, tc.wantPass)
			}
		})
	}
}

func TestMediaStatusIsTerminal(t *testing.T) {
	terminal := []MediaStatus{MediaDownloaded, MediaSkipped}
	active := []MediaStatus{MediaPending, MediaDownloading, MediaFailed}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("status %q should be terminal", s)
		}
	}
	for _, s := range active {
		if s.IsTerminal() {
			t.Errorf("status %q should not be terminal", s)
		}
	}
}

func TestMetadataFormatIsValid(t *testing.T) {
	valid := []MetadataFormat{MetadataMovie, MetadataEpisode}
	for _, f := range valid {
		if !f.IsValid() {
			t.Errorf("format %q should be valid", f)
		}
	}
	for _, f := range []MetadataFormat{"", "kodi", "emby"} {
		if f.IsValid() {
			t.Errorf("format %q should be invalid", f)
		}
	}
}
