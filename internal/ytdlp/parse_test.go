package ytdlp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseIndexLine(t *testing.T) {
	tests := []struct {
		name string
		json string
		want IndexEntry
	}{
		{
			name: "normal video",
			json: `{"id":"abc123","title":"My Video","description":"desc","uploader":"Chan","upload_date":"20240115","duration":125.0,"webpage_url":"https://youtube.com/watch?v=abc123"}`,
			want: IndexEntry{
				ExternalID:  "abc123",
				Title:       "My Video",
				Description: "desc",
				Uploader:    "Chan",
				UploadDate:  time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
				Duration:    125 * time.Second,
			},
		},
		{
			name: "short url sets IsShort",
			json: `{"id":"s1","title":"Short","webpage_url":"https://www.youtube.com/shorts/s1","duration":30}`,
			want: IndexEntry{
				ExternalID: "s1",
				Title:      "Short",
				Duration:   30 * time.Second,
				IsShort:    true,
			},
		},
		{
			name: "was_live flag sets IsLivestream",
			json: `{"id":"l1","title":"Stream","was_live":true,"duration":3600}`,
			want: IndexEntry{
				ExternalID:   "l1",
				Title:        "Stream",
				Duration:     3600 * time.Second,
				IsLivestream: true,
			},
		},
		{
			name: "live_status is_live sets IsLivestream",
			json: `{"id":"l2","title":"Live","live_status":"is_live"}`,
			want: IndexEntry{
				ExternalID:   "l2",
				Title:        "Live",
				IsLivestream: true,
			},
		},
		{
			name: "channel used when uploader absent",
			json: `{"id":"c1","title":"T","channel":"ChannelName"}`,
			want: IndexEntry{
				ExternalID: "c1",
				Title:      "T",
				Uploader:   "ChannelName",
			},
		},
		{
			name: "missing fields yield zero values",
			json: `{"id":"m1"}`,
			want: IndexEntry{ExternalID: "m1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIndexLine([]byte(tt.json))
			if err != nil {
				t.Fatalf("parseIndexLine() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseIndexLine() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIndexLineMalformed(t *testing.T) {
	if _, err := parseIndexLine([]byte("not json")); err == nil {
		t.Fatal("parseIndexLine() expected error for malformed JSON, got nil")
	}
}

func TestBuildIndexArgs(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		cookiesPath    string
		potProviderURL string
		want           []string
	}{
		{
			name: "without cookies",
			url:  "https://example.com/playlist",
			want: []string{"--dump-json", "--ignore-errors", "--flat-playlist", "https://example.com/playlist"},
		},
		{
			name:        "with cookies",
			url:         "https://example.com/playlist",
			cookiesPath: "/tmp/cookies.txt",
			want:        []string{"--dump-json", "--ignore-errors", "--flat-playlist", "--cookies", "/tmp/cookies.txt", "https://example.com/playlist"},
		},
		{
			name:           "with PO-token provider",
			url:            "https://example.com/playlist",
			potProviderURL: "http://pot:4416",
			want: []string{"--dump-json", "--ignore-errors", "--flat-playlist",
				"--extractor-args", "youtubepot-bgutilhttp:base_url=http://pot:4416", "https://example.com/playlist"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildIndexArgs(tt.url, IndexOptions{CookiesPath: tt.cookiesPath}, tt.potProviderURL)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildIndexArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildDownloadArgs(t *testing.T) {
	const url = "https://example.com/v"
	const out = "/media/out"
	progressTail := []string{
		"--newline",
		"--progress",
		"--progress-template", progressTemplateValue,
		"--print", "after_move:" + afterMovePrintPrefix + "%(filepath)s",
	}

	tests := []struct {
		name           string
		opts           DownloadOptions
		potProviderURL string
		want           []string
	}{
		{
			name: "video with explicit format",
			opts: DownloadOptions{Format: "bestvideo+bestaudio", OutputPath: out},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "bestvideo+bestaudio",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name:           "with PO-token provider",
			opts:           DownloadOptions{Format: "best", OutputPath: out},
			potProviderURL: "http://pot:4416",
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "best",
				"--extractor-args", "youtubepot-bgutilhttp:base_url=http://pot:4416",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "extra args appended verbatim",
			opts: DownloadOptions{Format: "best", OutputPath: out, ExtraArgs: []string{"--sleep-requests", "2"}},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "best",
				"--sleep-requests", "2",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "date-after cutoff added",
			opts: DownloadOptions{Format: "best", OutputPath: out, DateAfter: "20260101"},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "best",
				"--dateafter", "20260101",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "audio only uses extract audio",
			opts: DownloadOptions{AudioOnly: true, OutputPath: out},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-x",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "empty format omits -f",
			opts: DownloadOptions{OutputPath: out},
			want: append([]string{
				"-o", out + ".%(ext)s",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "embeds sponsorblock and cookies",
			opts: DownloadOptions{
				Format:           "best",
				OutputPath:       out,
				EmbedMetadata:    true,
				EmbedThumbnail:   true,
				EmbedSubtitles:   true,
				SubtitleLangs:    []string{"en", "es"},
				SponsorBlockArgs: []string{"--sponsorblock-remove", "sponsor"},
				CookiesPath:      "/c.txt",
			},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "best",
				"--embed-metadata",
				"--embed-thumbnail",
				"--embed-subs",
				"--sub-langs", "en,es",
				"--sponsorblock-remove", "sponsor",
				"--cookies", "/c.txt",
			}, append(append([]string{}, progressTail...), url)...),
		},
		{
			name: "embed subs without langs omits sub-langs",
			opts: DownloadOptions{Format: "best", OutputPath: out, EmbedSubtitles: true},
			want: append([]string{
				"-o", out + ".%(ext)s",
				"-f", "best",
				"--embed-subs",
			}, append(append([]string{}, progressTail...), url)...),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDownloadArgs(url, tt.opts, tt.potProviderURL)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildDownloadArgs() =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

func TestParseProgressPercent(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   float64
		wantOK bool
	}{
		// yt-dlp emits these tagged with our own marker; the "download:" in the
		// template selects which progress to report and is never printed.
		{name: "simple percent", line: progressLinePrefix + "42.5%", want: 42.5, wantOK: true},
		{name: "padded whitespace", line: progressLinePrefix + "  7.0%  ", want: 7.0, wantOK: true},
		{name: "hundred percent", line: progressLinePrefix + "100.0%", want: 100.0, wantOK: true},
		{name: "not a progress line", line: afterMovePrintPrefix + "/x/y.mp4", wantOK: false},
		{name: "unparseable value", line: progressLinePrefix + "N/A%", wantOK: false},
		{name: "blank", line: "", wantOK: false},
		// The bare percentage yt-dlp produced under the old template must not be
		// mistaken for a tagged line, and a "download:" prefix is not one either.
		{name: "untagged percentage from the old template", line: "  42.5%", wantOK: false},
		{name: "a literal download: prefix is not our marker", line: "download:42.5%", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseProgressPercent(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseProgressPercent() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseProgressPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAfterMovePath(t *testing.T) {
	// The marker comes from our own --print template. yt-dlp does NOT prefix the
	// output with "after_move:" — that word only selects when to print — so the
	// template embeds a marker we can match on.
	got, ok := parseAfterMovePath(afterMovePrintPrefix + "/media/final.mp4")
	if !ok || got != "/media/final.mp4" {
		t.Fatalf("parseAfterMovePath() = %q, %v; want /media/final.mp4, true", got, ok)
	}
	if _, ok := parseAfterMovePath("download:50.0%"); ok {
		t.Error("parseAfterMovePath() should not match a progress line")
	}
	if _, ok := parseAfterMovePath("after_move:/media/final.mp4"); ok {
		t.Error("parseAfterMovePath() must not match a bare after_move: line; yt-dlp never emits one")
	}
}

// TestScanDownloadOutputOnRealYtDlpOutput replays stdout captured from an actual
// yt-dlp run using the flags this package builds. The previous parser looked for
// an "after_move:" prefix that yt-dlp never emits, so it found no path for any
// download and every completed download was recorded as skipped. Pinning the
// real output shape here is what stops that from returning.
func TestScanDownloadOutputOnRealYtDlpOutput(t *testing.T) {
	// Captured from a real run: yt-dlp interleaves its own bracketed chatter with
	// the tagged lines this package asks for.
	const captured = "[youtube] Extracting URL: https://www.youtube.com/watch?v=2Q6OvYjOJi0\n" +
		progressLinePrefix + "   0.0%\n" +
		progressLinePrefix + "  45.3%\n" +
		"[Merger] Merging formats into \"GPS Hidden Messages.mkv\"\n" +
		progressLinePrefix + " 100.0%\n" +
		afterMovePrintPrefix + "/media/Computerphile/Season 2026/GPS Hidden Messages [2Q6OvYjOJi0].mkv\n"

	var percents []float64
	got := scanDownloadOutput(strings.NewReader(captured), func(p float64) {
		percents = append(percents, p)
	})

	want := "/media/Computerphile/Season 2026/GPS Hidden Messages [2Q6OvYjOJi0].mkv"
	if got != want {
		t.Fatalf("scanDownloadOutput() = %q, want %q", got, want)
	}
	if len(percents) != 3 {
		t.Errorf("progress callbacks = %d, want 3", len(percents))
	}
}

func TestDownloadArgsPrintTemplateCarriesTheMarker(t *testing.T) {
	// The marker has to travel inside the --print template, or nothing on stdout
	// identifies the finished file.
	args := buildDownloadArgs("https://example.com/v", DownloadOptions{OutputPath: "/media/out"}, "")

	if !containsPair(args, flagPrint, "after_move:"+afterMovePrintPrefix+"%(filepath)s") {
		t.Fatalf("--print template does not carry the marker: %v", args)
	}
}
