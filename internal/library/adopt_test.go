package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// placeFile writes a media file at the layout the default test profile renders,
// which is "{{ source_name }}/{{ title }}".
func placeFile(t *testing.T, h *harness, sourceName, title, ext string, size int) string {
	t.Helper()
	dir := filepath.Join(h.mediaDir, sourceName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, title+ext)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestReconcileAdoptsFilesAlreadyOnDisk(t *testing.T) {
	// The files are the real archive; the database is only a record of it. An item
	// recorded as skipped while its file sits on disk must be corrected, or the
	// video stays invisible and its bytes are never counted.
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "on-disk", Status: domain.MediaSkipped,
		Metadata: domain.MediaMetadata{Title: "A Downloaded Video"},
	})
	want := placeFile(t, h, "My Channel", "A Downloaded Video", ".mkv", 2048)

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.ExistingFilesAdopted != 1 {
		t.Fatalf("ExistingFilesAdopted = %d, want 1", report.ExistingFilesAdopted)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaDownloaded {
		t.Errorf("status = %q, want %q", media.Status, domain.MediaDownloaded)
	}
	if media.FilePath != want {
		t.Errorf("FilePath = %q, want %q", media.FilePath, want)
	}
	if media.FileSize != 2048 {
		t.Errorf("FileSize = %d, want 2048 so storage totals are right", media.FileSize)
	}
}

func TestReconcileLeavesGenuinelySkippedItemsAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "never-fetched", Status: domain.MediaSkipped,
		Metadata: domain.MediaMetadata{Title: "An Old Video"},
	})

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.ExistingFilesAdopted != 0 {
		t.Errorf("ExistingFilesAdopted = %d, want 0 — there is no file for it", report.ExistingFilesAdopted)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaSkipped {
		t.Errorf("status = %q, want it left as %q", media.Status, domain.MediaSkipped)
	}
}

func TestAdoptedItemsAreNotQueuedForDownloadAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "pending-but-present", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Already Here"},
	})
	placeFile(t, h, "My Channel", "Already Here", ".mp4", 512)

	if _, err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 0 {
		t.Errorf("enqueued %d downloads for a file already on disk, want 0", got)
	}
}

func TestDeleteSourceKeepsFilesByDefault(t *testing.T) {
	// Losing the records is recoverable — the files can be adopted again. Losing
	// the media is not, so it must never happen without being asked for.
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	path := placeFile(t, h, "My Channel", "Keep Me", ".mkv", 64)
	mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "keep", Status: domain.MediaDownloaded,
		FilePath: path, FileSize: 64,
		Metadata: domain.MediaMetadata{Title: "Keep Me"},
	})

	if err := h.svc.DeleteSource(ctx, sourceID, DeleteSourceOptions{}); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file was deleted without being asked for: %v", err)
	}
	if _, err := h.sources.Get(ctx, sourceID); err == nil {
		t.Error("the source should have been removed")
	}
}

func TestDeleteSourceRemovesFilesWhenAskedTo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	path := placeFile(t, h, "My Channel", "Delete Me", ".mkv", 64)
	mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "gone", Status: domain.MediaDownloaded,
		FilePath: path, FileSize: 64,
		Metadata: domain.MediaMetadata{Title: "Delete Me"},
	})

	if err := h.svc.DeleteSource(ctx, sourceID, DeleteSourceOptions{DeleteFiles: true}); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present after an explicit delete: %v", err)
	}
	// The emptied channel folder goes too, rather than lingering as clutter.
	if _, err := os.Stat(filepath.Join(h.mediaDir, "My Channel")); !os.IsNotExist(err) {
		t.Errorf("empty channel directory was left behind: %v", err)
	}
	// The media root itself must survive — it is a mount point, not ours to remove.
	if _, err := os.Stat(h.mediaDir); err != nil {
		t.Errorf("the media root was removed: %v", err)
	}
}

func TestDeleteSourceLeavesUnrelatedFilesAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mine := placeFile(t, h, "My Channel", "Mine", ".mkv", 32)
	// A file sub_scribe never recorded — someone else's, in the same folder.
	stranger := placeFile(t, h, "My Channel", "Not Mine", ".mkv", 32)
	mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "mine", Status: domain.MediaDownloaded,
		FilePath: mine, FileSize: 32, Metadata: domain.MediaMetadata{Title: "Mine"},
	})

	if err := h.svc.DeleteSource(ctx, sourceID, DeleteSourceOptions{DeleteFiles: true}); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	if _, err := os.Stat(stranger); err != nil {
		t.Errorf("deleted a file sub_scribe never recorded: %v", err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("the recorded file should be gone: %v", err)
	}
}

func TestAdoptionIgnoresPartialFiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "half-done", Status: domain.MediaFailed,
		Metadata: domain.MediaMetadata{Title: "Interrupted"},
	})
	placeFile(t, h, "My Channel", "Interrupted", ".mkv.part", 128)

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.ExistingFilesAdopted != 0 {
		t.Fatalf("adopted a partial download; report = %+v", report)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status == domain.MediaDownloaded {
		t.Error("a .part file was treated as a finished download")
	}
}
