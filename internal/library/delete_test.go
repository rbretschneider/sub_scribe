package library

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/ytdlp"
)

// seedDeletableVideo puts one downloaded video with a full sidecar set on disk
// and in the records, returning its id and file paths.
func seedDeletableVideo(t *testing.T, h *harness, sourceID int64, title string) (int64, string, []string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(h.mediaDir, "My Channel", "Season 2026")
	video := filepath.Join(dir, title+".mkv")
	sidecars := []string{
		filepath.Join(dir, title+".nfo"),
		filepath.Join(dir, title+".jpg"),
		filepath.Join(dir, title+".en.srt"),
	}
	writeFile(t, video)
	for _, sidecar := range sidecars {
		writeFile(t, sidecar)
	}
	id, _ := h.media.Upsert(ctx, domain.Media{
		SourceID: sourceID, ExternalID: title, Status: domain.MediaDownloaded, FilePath: video,
		Metadata: domain.MediaMetadata{Title: title, UploadDate: h.now},
	})
	return id, video, sidecars
}

func TestDeleteMediaRemovesTheFileItsSidecarsAndTombstonesTheRecord(t *testing.T) {
	h := newHarness(t)
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	id, video, sidecars := seedDeletableVideo(t, h, src.ID, "Junk Clip")
	// A neighbour proves the deletion is surgical.
	keptID, keptVideo, _ := seedDeletableVideo(t, h, src.ID, "Real Fight")

	if err := h.svc.DeleteMedia(context.Background(), id); err != nil {
		t.Fatalf("DeleteMedia: %v", err)
	}

	if exists(video) {
		t.Error("the video file survived deletion")
	}
	for _, sidecar := range sidecars {
		if exists(sidecar) {
			t.Errorf("sidecar %s survived deletion", filepath.Base(sidecar))
		}
	}
	if !exists(keptVideo) {
		t.Error("a neighbouring video was deleted")
	}

	got, _ := h.media.Get(context.Background(), id)
	if got.Status != domain.MediaDeleted {
		t.Errorf("status = %q, want the deleted tombstone", got.Status)
	}
	if got.FilePath != "" || got.FileSize != 0 {
		t.Errorf("file fields not cleared: %q / %d", got.FilePath, got.FileSize)
	}
	if kept, _ := h.media.Get(context.Background(), keptID); kept.Status != domain.MediaDownloaded {
		t.Errorf("neighbour status = %q, want downloaded untouched", kept.Status)
	}
}

func TestDeleteMediaRefusesAnItemInFlight(t *testing.T) {
	h := newHarness(t)
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))

	for _, status := range []domain.MediaStatus{domain.MediaPending, domain.MediaDownloading} {
		id, _ := h.media.Upsert(context.Background(), domain.Media{
			SourceID: src.ID, ExternalID: string(status), Status: status,
			Metadata: domain.MediaMetadata{Title: string(status)},
		})
		if err := h.svc.DeleteMedia(context.Background(), id); !errors.Is(err, ErrMediaInFlight) {
			t.Errorf("DeleteMedia(%s) err = %v, want ErrMediaInFlight", status, err)
		}
	}
}

// TestRescanNeverResurrectsADeletedVideo is the reason the tombstone exists:
// deleting the row instead would let the next scan rediscover the video and
// queue it again, making the deletion depend on the source's filters.
func TestRescanNeverResurrectsADeletedVideo(t *testing.T) {
	h := newHarness(t)
	profileID := seedEpisodeProfile(t, h)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	id, _, _ := seedDeletableVideo(t, h, src.ID, "Deleted On Purpose")

	if err := h.svc.DeleteMedia(context.Background(), id); err != nil {
		t.Fatalf("DeleteMedia: %v", err)
	}

	// The next scan offers the same video again.
	h.runner.entries = []ytdlp.IndexEntry{{
		ExternalID: "Deleted On Purpose", Title: "Deleted On Purpose", UploadDate: h.now,
	}}
	if err := h.svc.IndexSource(context.Background(), src.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	got, _ := h.media.Get(context.Background(), id)
	if got.Status != domain.MediaDeleted {
		t.Errorf("status after rescan = %q, want the tombstone to hold", got.Status)
	}
	if n := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); n != 0 {
		t.Errorf("rescan queued %d downloads for a deleted video, want 0", n)
	}
}
