package library

import (
	"context"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
)

// seedEpisodeProfile creates a series-style profile, the only kind for which a
// show-level sidecar means anything.
func seedEpisodeProfile(t *testing.T, h *harness) int64 {
	t.Helper()
	id, err := h.profiles.Create(context.Background(), domain.MediaProfile{
		Name:               "Plex TV",
		OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind:               domain.MediaVideo,
		QualityFormat:      "bestvideo+bestaudio",
		MetadataFormat:     domain.MetadataEpisode,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return id
}

// TestRefreshSidecarsRepairsAnExistingLibrary is the fix for the failure mode reported after
// the NFO work shipped: new downloads were described correctly and everything
// already in the library was not. Nothing about that looks broken from the
// outside — the files play, they are named right, the media server just shows
// the wrong titles — so it cannot be left to the user to notice and go looking
// for a button.
func TestRefreshSidecarsRepairsAnExistingLibrary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	path := filepath.Join(h.mediaDir, "My Channel", "Season 2026", "s2026e072401 - Old.mkv")
	writeFile(t, path)
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "a1", Status: domain.MediaDownloaded, FilePath: path,
		Metadata: domain.MediaMetadata{Title: "Old", UploadDate: h.now},
	})

	changed, err := h.svc.RefreshSidecars(ctx)
	if err != nil {
		t.Fatalf("RefreshSidecars: %v", err)
	}
	if changed != 1 {
		t.Errorf("sidecars refreshed = %d, want 1", changed)
	}
	if h.metadata.showCalls != 1 {
		t.Errorf("show sidecar writes = %d, want 1", h.metadata.showCalls)
	}
	if want := filepath.Join(h.mediaDir, "My Channel"); h.metadata.lastShowDir != want {
		t.Errorf("show sidecar written to %q, want the channel root %q", h.metadata.lastShowDir, want)
	}
}

// TestRefreshSidecarsWritesOneShowSidecarPerSource guards against the series file
// being rewritten once per episode, which on a large channel would be thousands
// of pointless writes to the same path.
func TestRefreshSidecarsWritesOneShowSidecarPerSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	for _, title := range []string{"One", "Two", "Three"} {
		path := filepath.Join(h.mediaDir, "My Channel", "Season 2026", title+".mkv")
		writeFile(t, path)
		h.media.Upsert(ctx, domain.Media{
			SourceID: src.ID, ExternalID: title, Status: domain.MediaDownloaded, FilePath: path,
			Metadata: domain.MediaMetadata{Title: title, UploadDate: h.now},
		})
	}

	if _, err := h.svc.RefreshSidecars(ctx); err != nil {
		t.Fatalf("RefreshSidecars: %v", err)
	}
	if h.metadata.showCalls != 1 {
		t.Errorf("show sidecar writes = %d, want 1 for a source with three episodes", h.metadata.showCalls)
	}
}

// TestRefreshSidecarsSkipsItemsWithNoFile keeps the pass to things that exist: an item
// still queued has nothing to write a sidecar beside.
func TestRefreshSidecarsSkipsItemsWithNoFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "p1", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Queued", UploadDate: h.now},
	})

	changed, err := h.svc.RefreshSidecars(ctx)
	if err != nil {
		t.Fatalf("RefreshSidecars: %v", err)
	}
	if changed != 0 {
		t.Errorf("sidecars refreshed = %d, want 0", changed)
	}
	if h.metadata.showCalls != 0 {
		t.Errorf("show sidecar writes = %d, want 0 when nothing is downloaded", h.metadata.showCalls)
	}
}

// TestRefreshSidecarsCountsOnlyChangedFiles: a startup that rewrote nothing must
// report nothing, or every boot logs a repair that did not happen and the log
// stops being a signal.
func TestRefreshSidecarsCountsOnlyChangedFiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	path := filepath.Join(h.mediaDir, "My Channel", "Season 2026", "Already Current.mkv")
	writeFile(t, path)
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "a1", Status: domain.MediaDownloaded, FilePath: path,
		Metadata: domain.MediaMetadata{Title: "Already Current", UploadDate: h.now},
	})
	h.metadata.unchanged = true // the writer finds the file already correct

	changed, err := h.svc.RefreshSidecars(ctx)
	if err != nil {
		t.Fatalf("RefreshSidecars: %v", err)
	}
	if changed != 0 {
		t.Errorf("sidecars refreshed = %d, want 0 when nothing needed rewriting", changed)
	}
}

// TestReconcileRePointsAMovedFile covers a stale recorded path. Until it is
// corrected, everything downstream aims at a file that is not there: sidecars
// are written beside nothing, and the video that does exist stays undescribed.
func TestReconcileRePointsAMovedFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	// On disk where the template says it belongs; recorded somewhere it is not.
	actual := filepath.Join(h.mediaDir, "My Channel", "Moved.mkv")
	writeFile(t, actual)
	mediaID, _ := h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "m1", Status: domain.MediaDownloaded,
		FilePath: filepath.Join(h.mediaDir, "My Channel", "old location.mkv"),
		Metadata: domain.MediaMetadata{Title: "Moved", UploadDate: h.now},
	})

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.MovedFilesRepaired != 1 {
		t.Errorf("moved files repaired = %d, want 1", report.MovedFilesRepaired)
	}
	got, _ := h.media.Get(ctx, mediaID)
	if got.FilePath != actual {
		t.Errorf("recorded path = %q, want %q", got.FilePath, actual)
	}
}

// TestReconcileLeavesGenuinelyMissingFilesAlone is the safety property. A media
// volume that failed to mount makes every file look missing; responding by
// re-downloading the archive would be far worse than the stale path.
func TestReconcileLeavesGenuinelyMissingFilesAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	recorded := filepath.Join(h.mediaDir, "My Channel", "gone.mkv")
	mediaID, _ := h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "g1", Status: domain.MediaDownloaded, FilePath: recorded,
		Metadata: domain.MediaMetadata{Title: "Gone", UploadDate: h.now},
	})

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.MovedFilesRepaired != 0 {
		t.Errorf("moved files repaired = %d, want 0", report.MovedFilesRepaired)
	}
	got, _ := h.media.Get(ctx, mediaID)
	if got.Status != domain.MediaDownloaded || got.FilePath != recorded {
		t.Errorf("a missing file was disturbed: status=%q path=%q", got.Status, got.FilePath)
	}
}
