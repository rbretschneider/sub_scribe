package metadata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

func TestNFOPlot_AC9_WatchURLIsAppendedAfterAnyDescription(t *testing.T) {
	url := domain.WatchURL("abc123")
	for name, desc := range map[string]string{
		"cleaned text": "Kept paragraph",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			m := sampleMedia()
			m.Metadata.Description = desc
			body, err := BuildNFO(m, "Chan", domain.MetadataEpisode, SeasonEpisode{})
			if err != nil {
				t.Fatal(err)
			}
			plot := parsedNFO(t, body).Plot
			if desc == "" {
				if plot != url {
					t.Errorf("plot = %q, want just the watch URL", plot)
				}
				return
			}
			if !strings.HasSuffix(plot, "\n\n"+url) {
				t.Errorf("plot = %q, want it to end with a blank line and %s", plot, url)
			}
			if desc != "" && !strings.HasPrefix(plot, desc) {
				t.Errorf("plot = %q, want it to start with the description", plot)
			}
		})
	}
}

func TestWriteFor_UnchangedSidecarKeepsItsTimestampAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "video.mkv")
	w := NewWriter()
	ctx := context.Background()
	changed, err := w.WriteFor(ctx, media, sampleMedia(), "Chan", domain.MetadataEpisode)
	if err != nil || !changed {
		t.Fatalf("first write = %v, %v; want changed", changed, err)
	}
	nfo := filepath.Join(dir, "video.nfo")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(nfo, old, old)
	changed, err = w.WriteFor(ctx, media, sampleMedia(), "Chan", domain.MetadataEpisode)
	if err != nil || changed {
		t.Errorf("second write = %v, %v; want unchanged", changed, err)
	}
	if info, _ := os.Stat(nfo); !info.ModTime().Equal(old) {
		t.Error("an unchanged sidecar must not be restamped")
	}

	m := sampleMedia()
	m.Metadata.Description = "different"
	if changed, _ := w.WriteFor(ctx, media, m, "Chan", domain.MetadataEpisode); !changed {
		t.Error("changed content must be written")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "video.nfo" {
			t.Errorf("unexpected file %q left beside the sidecar", e.Name())
		}
	}
}

func TestWriteShow_IsAtomicAndSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter()
	if changed, err := w.WriteShow(context.Background(), dir, "Chan", "https://example.test"); err != nil || !changed {
		t.Fatalf("first = %v, %v", changed, err)
	}
	if changed, _ := w.WriteShow(context.Background(), dir, "Chan", "https://example.test"); changed {
		t.Error("unchanged show file rewritten")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1", len(entries))
	}
}
