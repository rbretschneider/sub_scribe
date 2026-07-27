package library

import (
	"context"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/ytdlp"
)

func TestWideningTheWindowRequeuesPreviouslySkippedVideos(t *testing.T) {
	// The complaint this fixes: widen a source's window, rescan, and nothing
	// happens. The scan finds the videos, sees rows already exist, and queues
	// none of them — so the setting appears to do nothing at all.
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	input := validInput(profileID)
	// The window has already been widened to 90 days; the video below was passed
	// over while it was still 30.
	input.CutoffWindow = 90 * 24 * time.Hour
	source, err := h.svc.AddSource(ctx, input)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	// An older video, passed over by an earlier and narrower scan.
	older := h.now.AddDate(0, 0, -60)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: source.ID, ExternalID: "older", Status: domain.MediaSkipped,
		Metadata: domain.MediaMetadata{Title: "An Older Video", UploadDate: older},
	})
	h.tasks.tasks = nil

	// The window is now wide enough that the scan offers it again.
	h.runner.entries = []ytdlp.IndexEntry{
		{ExternalID: "older", Title: "An Older Video", UploadDate: older},
	}
	if err := h.svc.IndexSource(ctx, source.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaPending {
		t.Errorf("status = %q, want %q so it will be downloaded", media.Status, domain.MediaPending)
	}
	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 1 {
		t.Fatalf("enqueued %d downloads, want 1", got)
	}
}

func TestRescanLeavesSettledVideosAlone(t *testing.T) {
	// A routine rescan must not disturb what is already downloaded or waiting,
	// or every scan would re-fetch the whole archive.
	for _, status := range []domain.MediaStatus{
		domain.MediaDownloaded, domain.MediaPending, domain.MediaFailed, domain.MediaUnavailable,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			sourceID := h.seedSource(t)
			mustUpsertMedia(t, h, domain.Media{
				SourceID: sourceID, ExternalID: "known", Status: status,
				Metadata: domain.MediaMetadata{Title: "Known", UploadDate: h.now},
			})
			h.tasks.tasks = nil
			h.runner.entries = []ytdlp.IndexEntry{
				{ExternalID: "known", Title: "Known", UploadDate: h.now},
			}

			if err := h.svc.IndexSource(ctx, sourceID); err != nil {
				t.Fatalf("IndexSource: %v", err)
			}

			if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 0 {
				t.Errorf("enqueued %d downloads for a %s video, want 0", got, status)
			}
		})
	}
}
