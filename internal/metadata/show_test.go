package metadata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSeasonEpisode(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		wantSeason  int
		wantEpisode int
		wantOK      bool
	}{
		{
			name: "the layout sub_scribe writes", file: "s2026e072401 - Some Video.mkv",
			wantSeason: 2026, wantEpisode: 72401, wantOK: true,
		},
		{
			name: "a full path", file: "/media/Chan/Season 2026/s2026e010203 - Title.mkv",
			wantSeason: 2026, wantEpisode: 10203, wantOK: true,
		},
		{
			name: "conventional short numbering", file: "s01e02 - Pilot.mkv",
			wantSeason: 1, wantEpisode: 2, wantOK: true,
		},
		{name: "uppercase", file: "S2026E072401 - Title.mkv", wantSeason: 2026, wantEpisode: 72401, wantOK: true},
		// The old date-based layout carries no season/episode token, and inventing
		// one would put a number in the NFO that the filename contradicts.
		{name: "the old date-based layout", file: "Chan - 2026-04-18 - Title [abc].mkv", wantOK: false},
		{name: "no numbering at all", file: "Just A Title.mkv", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseSeasonEpisode(tt.file)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Season != tt.wantSeason || got.Episode != tt.wantEpisode {
				t.Errorf("got s%de%d, want s%de%d", got.Season, got.Episode, tt.wantSeason, tt.wantEpisode)
			}
		})
	}
}

// TestEpisodeNFODeclaresItsNumbering matters for the Plex NFO agent, which reads
// the sidecar rather than guessing from the filename. Without these elements the
// episode has no place in the season and shows up as an unnamed extra.
func TestEpisodeNFODeclaresItsNumbering(t *testing.T) {
	body, err := BuildEpisodeNFO(sampleMedia(), "Chan", SeasonEpisode{Season: 2026, Episode: 72401})
	if err != nil {
		t.Fatalf("BuildEpisodeNFO: %v", err)
	}
	for _, want := range []string{"<season>2026</season>", "<episode>72401</episode>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("nfo is missing %s:\n%s", want, body)
		}
	}
}

// TestEpisodeNFOOmitsUnknownNumbering guards the other direction: a zero written
// out as <season>0</season> is a claim, and a wrong one.
func TestEpisodeNFOOmitsUnknownNumbering(t *testing.T) {
	body, err := BuildEpisodeNFO(sampleMedia(), "Chan", SeasonEpisode{})
	if err != nil {
		t.Fatalf("BuildEpisodeNFO: %v", err)
	}
	for _, unwanted := range []string{"<season>", "<episode>"} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("nfo asserts %s despite not knowing the numbering:\n%s", unwanted, body)
		}
	}
}

func TestBuildShowNFONamesTheSeries(t *testing.T) {
	body, err := BuildShowNFO("Channel 5 with Andrew Callaghan", "https://www.youtube.com/@Channel5YouTube")
	if err != nil {
		t.Fatalf("BuildShowNFO: %v", err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "<?xml") {
		t.Errorf("show nfo has no XML header:\n%s", text)
	}
	// The title is the entire point: without it a media server matches the folder
	// name against an online database, which is how a YouTube channel ends up
	// identified as an unrelated anime.
	if !strings.Contains(text, "<title>Channel 5 with Andrew Callaghan</title>") {
		t.Errorf("show nfo does not state the series title:\n%s", text)
	}
	if !strings.Contains(text, "<tvshow>") {
		t.Errorf("show nfo root element is not <tvshow>:\n%s", text)
	}
}

func TestWriteShowCreatesTvshowNFOAtTheChannelRoot(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter()

	if _, err := writer.WriteShow(context.Background(), dir, "Chan", "https://example.com/@chan"); err != nil {
		t.Fatalf("WriteShow: %v", err)
	}

	// The name is fixed: a media server looks for exactly "tvshow.nfo".
	body, err := os.ReadFile(filepath.Join(dir, "tvshow.nfo"))
	if err != nil {
		t.Fatalf("read tvshow.nfo: %v", err)
	}
	if !strings.Contains(string(body), "<title>Chan</title>") {
		t.Errorf("tvshow.nfo does not name the series:\n%s", body)
	}
}

// TestWriteShowLeavesUnchangedFileAlone keeps a rescan from being triggered on
// every single download: rewriting identical bytes still restamps the file.
func TestWriteShowLeavesUnchangedFileAlone(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter()
	ctx := context.Background()
	path := filepath.Join(dir, "tvshow.nfo")

	if _, err := writer.WriteShow(ctx, dir, "Chan", "https://example.com/@chan"); err != nil {
		t.Fatalf("WriteShow: %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if _, err := writer.WriteShow(ctx, dir, "Chan", "https://example.com/@chan"); err != nil {
		t.Fatalf("WriteShow (second): %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Error("an unchanged tvshow.nfo was rewritten, which restamps it and invites a rescan")
	}
}

func TestWriteShowUpdatesWhenTheNameChanges(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter()
	ctx := context.Background()

	if _, err := writer.WriteShow(ctx, dir, "Old Name", "https://example.com/@chan"); err != nil {
		t.Fatalf("WriteShow: %v", err)
	}
	if _, err := writer.WriteShow(ctx, dir, "New Name", "https://example.com/@chan"); err != nil {
		t.Fatalf("WriteShow (renamed): %v", err)
	}

	body, _ := os.ReadFile(filepath.Join(dir, "tvshow.nfo"))
	if !strings.Contains(string(body), "<title>New Name</title>") {
		t.Errorf("a renamed source did not update its tvshow.nfo:\n%s", body)
	}
}

// TestWriteForTakesNumberingFromTheFileName pins the rule that the sidecar never
// contradicts the name beside it. If the two disagree the media server picks one
// and the user has no way to tell which.
func TestWriteForTakesNumberingFromTheFileName(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "s2026e072401 - Some Video.mkv")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	if _, err := NewWriter().WriteFor(context.Background(), mediaPath, sampleMedia(), "Chan", ""); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "s2026e072401 - Some Video.nfo"))
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}
	for _, want := range []string{"<season>2026</season>", "<episode>72401</episode>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("nfo is missing %s taken from the file name:\n%s", want, body)
		}
	}
}
