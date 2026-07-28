package library

import (
	"context"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
)

// orphanHarness sets up a source with one downloaded video, and returns the
// folder its files live in.
func orphanHarness(t *testing.T) (*harness, int64, string) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	dir := filepath.Join(h.mediaDir, "My Channel", "Season 2026")
	video := filepath.Join(dir, "Kept.mkv")
	writeFile(t, video)
	writeFile(t, filepath.Join(dir, "Kept.nfo"))
	writeFile(t, filepath.Join(dir, "Kept.jpg"))
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "k1", Status: domain.MediaDownloaded, FilePath: video,
		Metadata: domain.MediaMetadata{Title: "Kept", UploadDate: h.now},
	})
	return h, src.ID, dir
}

// TestSweepRemovesSidecarsWithNoVideo covers the accumulation: a video deleted by
// hand or by a retention pass leaves its metadata behind, describing nothing, and
// a media server scanning the folder can attach it to the wrong episode.
func TestSweepRemovesSidecarsWithNoVideo(t *testing.T) {
	h, sourceID, dir := orphanHarness(t)
	writeFile(t, filepath.Join(dir, "Deleted Video.nfo"))
	writeFile(t, filepath.Join(dir, "Deleted Video.jpg"))

	removed, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID)
	if err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if exists(filepath.Join(dir, "Deleted Video.nfo")) || exists(filepath.Join(dir, "Deleted Video.jpg")) {
		t.Error("an orphaned sidecar survived the sweep")
	}
}

// TestSweepKeepsSidecarsThatHaveTheirVideo is the property that matters most:
// this deletes files, so it must never take one that is doing its job.
func TestSweepKeepsSidecarsThatHaveTheirVideo(t *testing.T) {
	h, sourceID, dir := orphanHarness(t)

	if _, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID); err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	for _, name := range []string{"Kept.mkv", "Kept.nfo", "Kept.jpg"} {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("%s was deleted although its video is right there", name)
		}
	}
}

// TestSweepNeverTakesTheSeriesFile: tvshow.nfo belongs to the folder, not to any
// video, so by the sweep's own rule it looks exactly like an orphan.
func TestSweepNeverTakesTheSeriesFile(t *testing.T) {
	h, sourceID, dir := orphanHarness(t)
	showFile := filepath.Join(dir, "tvshow.nfo")
	writeFile(t, showFile)

	if _, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID); err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	if !exists(showFile) {
		t.Error("the sweep deleted tvshow.nfo, which no video owns by design")
	}
}

// TestSweepLeavesFilesItDidNotWrite keeps the blast radius to sub_scribe's own
// output. Someone else's subtitles, artwork, or notes in the same folder are not
// ours to tidy up.
func TestSweepLeavesFilesItDidNotWrite(t *testing.T) {
	h, sourceID, dir := orphanHarness(t)
	strangers := []string{"notes.txt", "Something Else.srt", "backup.mkv", "art.png"}
	for _, name := range strangers {
		writeFile(t, filepath.Join(dir, name))
	}

	if _, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID); err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	for _, name := range strangers {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("the sweep deleted %s, which sub_scribe never wrote", name)
		}
	}
}

// TestSweepOnlyVisitsFoldersWeDownloadInto bounds where it looks at all. A folder
// with no downloads of ours in it is not somewhere to be deleting files.
func TestSweepOnlyVisitsFoldersWeDownloadInto(t *testing.T) {
	h, sourceID, _ := orphanHarness(t)
	elsewhere := filepath.Join(h.mediaDir, "Someone Elses Show", "Season 1")
	writeFile(t, filepath.Join(elsewhere, "orphan.nfo"))

	if _, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID); err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	if !exists(filepath.Join(elsewhere, "orphan.nfo")) {
		t.Error("the sweep reached into a folder sub_scribe has never downloaded into")
	}
}

// TestSweepKeepsMultiPartSuffixes: a subtitle named "Video.en.srt" belongs to
// "Video.mkv", and matching on the whole extension alone would miss that.
func TestSweepKeepsMultiPartSuffixes(t *testing.T) {
	h, sourceID, dir := orphanHarness(t)
	subtitle := filepath.Join(dir, "Kept.en.jpg") // a sidecar extension, still owned
	writeFile(t, subtitle)

	if _, err := h.svc.sweepOrphanedSidecars(context.Background(), sourceID); err != nil {
		t.Fatalf("sweepOrphanedSidecars: %v", err)
	}
	if !exists(subtitle) {
		t.Error("a sidecar with a multi-part suffix was taken from the video that owns it")
	}
}
