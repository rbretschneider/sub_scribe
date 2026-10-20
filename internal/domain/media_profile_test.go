package domain

import "testing"

func TestRequestedHeightCap(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   int
	}{
		{"standard capped selector", "bestvideo[height<=1080]+bestaudio/best", 1080},
		{"strict bound counts too", "bestvideo[height<720]+bestaudio", 720},
		{"spaces inside the bound", "bestvideo[height <= 2160]+bestaudio", 2160},
		{"uncapped selector has no bound", "bestvideo+bestaudio", 0},
		{"plain best has no bound", "best", 0},
		{"empty selector has no bound", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := MediaProfile{QualityFormat: tt.format}
			if got := p.RequestedHeightCap(); got != tt.want {
				t.Errorf("RequestedHeightCap(%q) = %d, want %d", tt.format, got, tt.want)
			}
		})
	}
}

func TestIsDegradedDownload(t *testing.T) {
	tests := []struct {
		name   string
		format string
		height int
		want   bool
	}{
		// The incident shape: an HD profile receiving the 360p "/best" fallback.
		{"1080p profile gets 360p", "bestvideo[height<=1080]+bestaudio/best", 360, true},
		{"1080p profile gets 480p", "bestvideo[height<=1080]+bestaudio/best", 480, true},
		{"720p profile gets 360p", "bestvideo[height<=720]+bestaudio", 360, true},
		// An uncapped bestvideo selector asks for the best there is; 360p is
		// just as degraded for it.
		{"uncapped bestvideo gets 360p", "bestvideo+bestaudio", 360, true},

		// Normal outcomes that must never be flagged.
		{"1080p profile gets 1080p", "bestvideo[height<=1080]+bestaudio/best", 1080, false},
		{"1080p profile gets 720p", "bestvideo[height<=1080]+bestaudio/best", 720, false},
		{"old upload genuinely tops out above the ceiling", "bestvideo[height<=1080]+bestaudio/best", 540, false},
		// A profile content with SD can only ever get SD.
		{"480p profile gets 360p", "bestvideo[height<=480]+bestaudio", 360, false},
		// "best" carries no quality demand at all.
		{"plain best gets 360p", "best", 360, false},
		// Unknown height: audio-only downloads and files adopted from disk.
		{"unknown height is never degraded", "bestvideo[height<=1080]+bestaudio/best", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := MediaProfile{QualityFormat: tt.format}
			if got := p.IsDegradedDownload(tt.height); got != tt.want {
				t.Errorf("IsDegradedDownload(%d) with %q = %v, want %v", tt.height, tt.format, got, tt.want)
			}
		})
	}
}
