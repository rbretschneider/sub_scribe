package ytdlp

import (
	"reflect"
	"testing"
)

// channelJSON is the shape yt-dlp reports for a YouTube channel: the full-size
// avatar and banner alongside the smaller crops YouTube uses in its own layout.
const channelJSON = `{
  "id": "@SomeChannel",
  "title": "Some Channel",
  "thumbnails": [
    {"url": "https://img/avatar-small", "id": "avatar_uncropped", "width": 176},
    {"url": "https://img/avatar-large", "id": "avatar_uncropped", "width": 900},
    {"url": "https://img/banner", "id": "banner_uncropped", "width": 2560}
  ]
}`

func TestParseArtworkPrefersTheLargestAvatarAndBanner(t *testing.T) {
	art, err := parseArtwork([]byte(channelJSON))
	if err != nil {
		t.Fatalf("parseArtwork: %v", err)
	}

	if art.PosterURL != "https://img/avatar-large" {
		t.Errorf("PosterURL = %q, want the full-size avatar", art.PosterURL)
	}
	if art.BackgroundURL != "https://img/banner" {
		t.Errorf("BackgroundURL = %q, want the banner", art.BackgroundURL)
	}
}

// TestParseArtworkFallsBackToAPlaylistStill covers a playlist, which has no
// avatar at all — a frame from one of its videos is a far better poster than the
// blank placeholder a media server shows without one.
func TestParseArtworkFallsBackToAPlaylistStill(t *testing.T) {
	const playlistJSON = `{"thumbnails": [{"url": "https://img/still", "id": "0", "width": 640}]}`

	art, err := parseArtwork([]byte(playlistJSON))
	if err != nil {
		t.Fatalf("parseArtwork: %v", err)
	}

	if art.PosterURL != "https://img/still" {
		t.Errorf("PosterURL = %q, want the playlist still", art.PosterURL)
	}
}

// TestParseArtworkNeverUsesABannerAsAPoster guards the fallback: a banner is the
// wrong shape entirely, and stretched across a poster slot it looks broken.
func TestParseArtworkNeverUsesABannerAsAPoster(t *testing.T) {
	const bannerOnlyJSON = `{"thumbnails": [{"url": "https://img/banner", "id": "banner_uncropped", "width": 2560}]}`

	art, err := parseArtwork([]byte(bannerOnlyJSON))
	if err != nil {
		t.Fatalf("parseArtwork: %v", err)
	}

	if art.PosterURL != "" {
		t.Errorf("PosterURL = %q, want no poster rather than a banner", art.PosterURL)
	}
	if art.BackgroundURL != "https://img/banner" {
		t.Errorf("BackgroundURL = %q, want the banner", art.BackgroundURL)
	}
}

// TestParseArtworkWithoutThumbnailsIsNotAnError separates "this channel has no
// pictures", which is normal, from a failure worth reporting.
func TestParseArtworkWithoutThumbnailsIsNotAnError(t *testing.T) {
	art, err := parseArtwork([]byte(`{"id": "@Bare"}`))
	if err != nil {
		t.Fatalf("parseArtwork: %v", err)
	}
	if !art.IsEmpty() {
		t.Errorf("artwork = %+v, want empty", art)
	}
}

func TestParseArtworkRejectsMalformedJSON(t *testing.T) {
	if _, err := parseArtwork([]byte("not json")); err == nil {
		t.Fatal("parseArtwork succeeded, want an error for malformed JSON")
	}
}

// TestBuildArtworkArgsDoesNotWalkTheCollection is the cost guarantee: asking a
// thousand-video channel for its avatar must not enumerate a thousand videos.
func TestBuildArtworkArgsDoesNotWalkTheCollection(t *testing.T) {
	got := buildArtworkArgs("https://example.com/@chan", "", "", Throttle{})

	want := []string{"--dump-single-json", "--flat-playlist", "--playlist-items", "0", "https://example.com/@chan"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestBuildArtworkArgsPassesCookies(t *testing.T) {
	got := buildArtworkArgs("https://example.com/@chan", "/tmp/cookies.txt", "", Throttle{})

	want := []string{
		"--dump-single-json", "--flat-playlist", "--playlist-items", "0",
		"--cookies", "/tmp/cookies.txt", "https://example.com/@chan",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
