package library

import (
	"context"
	"errors"
	"testing"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

func TestDownloadVideoCreatesTheBucketAndQueuesTheVideo(t *testing.T) {
	h := newHarness(t)
	h.seedProfile(t)
	ctx := context.Background()

	id, err := h.svc.DownloadVideo(ctx, "https://www.youtube.com/watch?v=gCZOjDar1tU")
	if err != nil {
		t.Fatalf("DownloadVideo: %v", err)
	}

	media, err := h.media.Get(ctx, id)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.ExternalID != "gCZOjDar1tU" || media.Status != domain.MediaPending {
		t.Errorf("media = %+v, want pending gCZOjDar1tU", media)
	}

	source, err := h.sources.Get(ctx, media.SourceID)
	if err != nil {
		t.Fatalf("get singles source: %v", err)
	}
	if source.CollectionType != domain.CollectionSingles {
		t.Errorf("bucket type = %q, want %q", source.CollectionType, domain.CollectionSingles)
	}

	downloads := h.tasks.tasksOfType(jobs.TaskDownloadMedia)
	if len(downloads) != 1 {
		t.Fatalf("queued %d downloads, want 1", len(downloads))
	}
	if downloads[0].Priority != scanNowPriority {
		t.Errorf("priority = %d, want front-of-line %d", downloads[0].Priority, scanNowPriority)
	}
}

func TestDownloadVideoReusesOneBucketAcrossVideos(t *testing.T) {
	h := newHarness(t)
	h.seedProfile(t)
	ctx := context.Background()

	first, err := h.svc.DownloadVideo(ctx, "https://youtu.be/aaaaaaaaaaa")
	if err != nil {
		t.Fatalf("first DownloadVideo: %v", err)
	}
	second, err := h.svc.DownloadVideo(ctx, "https://youtu.be/bbbbbbbbbbb")
	if err != nil {
		t.Fatalf("second DownloadVideo: %v", err)
	}

	firstMedia, _ := h.media.Get(ctx, first)
	secondMedia, _ := h.media.Get(ctx, second)
	if firstMedia.SourceID != secondMedia.SourceID {
		t.Errorf("videos landed in different buckets: %d vs %d", firstMedia.SourceID, secondMedia.SourceID)
	}

	sources, _ := h.sources.List(ctx)
	singles := 0
	for _, source := range sources {
		if source.CollectionType == domain.CollectionSingles {
			singles++
		}
	}
	if singles != 1 {
		t.Errorf("found %d singles buckets, want exactly 1", singles)
	}
}

func TestDownloadVideoRepeatIsIdempotentWhileHealthy(t *testing.T) {
	h := newHarness(t)
	h.seedProfile(t)
	ctx := context.Background()

	first, err := h.svc.DownloadVideo(ctx, "gCZOjDar1tU")
	if err != nil {
		t.Fatalf("DownloadVideo: %v", err)
	}
	again, err := h.svc.DownloadVideo(ctx, "https://youtu.be/gCZOjDar1tU")
	if err != nil {
		t.Fatalf("repeat DownloadVideo: %v", err)
	}

	if again != first {
		t.Errorf("repeat returned media %d, want the original %d", again, first)
	}
	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 1 {
		t.Errorf("repeat queued a second download: %d tasks", got)
	}
}

func TestDownloadVideoGivesAFailedVideoAFreshAttempt(t *testing.T) {
	h := newHarness(t)
	h.seedProfile(t)
	ctx := context.Background()

	id, err := h.svc.DownloadVideo(ctx, "gCZOjDar1tU")
	if err != nil {
		t.Fatalf("DownloadVideo: %v", err)
	}
	if err := h.media.SetStatus(ctx, id, domain.MediaFailed, h.now); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	again, err := h.svc.DownloadVideo(ctx, "gCZOjDar1tU")
	if err != nil {
		t.Fatalf("retry DownloadVideo: %v", err)
	}
	if again != id {
		t.Errorf("retry returned media %d, want %d", again, id)
	}
	media, _ := h.media.Get(ctx, id)
	if media.Status != domain.MediaPending {
		t.Errorf("status = %q, want pending again", media.Status)
	}
	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 2 {
		t.Errorf("expected a second download task, got %d", got)
	}
}

func TestDownloadVideoRejectsCollectionsAndGarbage(t *testing.T) {
	h := newHarness(t)
	h.seedProfile(t)

	for _, raw := range []string{
		"https://www.youtube.com/@Channel5YouTube",
		"https://www.youtube.com/playlist?list=PLx",
		"not a url",
	} {
		if _, err := h.svc.DownloadVideo(context.Background(), raw); !errors.Is(err, ErrNotAVideoURL) {
			t.Errorf("DownloadVideo(%q) err = %v, want ErrNotAVideoURL", raw, err)
		}
	}
}
