package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/events"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/ytdlp"
)

// requireDeferral asserts err carries a jobs.Deferral and returns it.
func requireDeferral(t *testing.T, err error) *jobs.Deferral {
	t.Helper()
	var deferral *jobs.Deferral
	if !errors.As(err, &deferral) {
		t.Fatalf("expected a jobs.Deferral, got %v", err)
	}
	return deferral
}

// seedPendingMedia inserts a pending item for the source and returns its id.
func seedPendingMedia(t *testing.T, h *harness, sourceID int64) int64 {
	t.Helper()
	id, err := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   sourceID,
		ExternalID: "abc123",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Cool Video", UploadDate: h.now},
	})
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	return id
}

// TestDownloadMediaBlockedByA403FailsFastAndEscalates pins the response to a
// download the provider refused with HTTP 403 under every client: it settles
// immediately instead of spending paced retries, and it is escalated — a run
// of 403s otherwise stretched silent failures across hours of 8–12 minute
// download slots before anyone saw one.
func TestDownloadMediaBlockedByA403FailsFastAndEscalates(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID := seedPendingMedia(t, h, src.ID)
	h.runner.downloadErr = fmt.Errorf("%w: HTTP Error 403", ytdlp.ErrAccessDenied)

	err := h.svc.DownloadMedia(context.Background(), mediaID)

	// A nil error settles the task: no retry budget is spent waiting behind
	// the download pacing to hear the same refusal again.
	if err != nil {
		t.Fatalf("DownloadMedia() = %v, want nil so the task is not retried", err)
	}
	got, _ := h.media.Get(context.Background(), mediaID)
	if got.Status != domain.MediaFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.LastError == "" {
		t.Error("expected the refusal recorded on the item")
	}
	if h.pub.countKind(events.KindMediaFailed) != 1 {
		t.Errorf("media_failed events = %d, want 1", h.pub.countKind(events.KindMediaFailed))
	}
	if h.notifier.calls != 1 {
		t.Errorf("notifications = %d, want the block escalated to the notifier", h.notifier.calls)
	}
}

// TestDownloadMediaPacingDeferralIsRoutine pins that waiting for a download
// slot is marked as ordinary scheduling, which is what keeps the worker from
// logging a claim/defer pair for every queued download at every slot.
func TestDownloadMediaPacingDeferralIsRoutine(t *testing.T) {
	h := newHarness(t)
	h.pace = &stubPacer{allow: false, slot: h.now.Add(10 * time.Minute)}
	h.rebuildService()
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID := seedPendingMedia(t, h, src.ID)

	err := h.svc.DownloadMedia(context.Background(), mediaID)

	deferral := requireDeferral(t, err)
	if !deferral.Routine {
		t.Error("a pacing wait must be a routine deferral, or it floods the log")
	}
}

// TestThrottleDeferralIsNotRoutine pins the contrast: backing off from a rate
// limit is news worth a log line, unlike waiting out the deliberate spacing.
func TestThrottleDeferralIsNotRoutine(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID := seedPendingMedia(t, h, src.ID)
	h.runner.downloadErr = fmt.Errorf("%w: HTTP Error 429", ytdlp.ErrThrottled)

	err := h.svc.DownloadMedia(context.Background(), mediaID)

	if deferral := requireDeferral(t, err); deferral.Routine {
		t.Error("a throttle backoff must not be routine — it should be visible in the log")
	}
}

// TestNoArtworkFetchForASourceWithoutAURL pins that the singles bucket — the
// one source with no remote collection — never asks yt-dlp for the artwork of
// "", which failed (and logged an error) after every one-off download.
func TestNoArtworkFetchForASourceWithoutAURL(t *testing.T) {
	h := newHarness(t)
	profileID, err := h.profiles.Create(context.Background(), domain.MediaProfile{
		Name:               "Series",
		OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind:               domain.MediaVideo,
		QualityFormat:      "bestvideo+bestaudio",
		MetadataFormat:     domain.MetadataEpisode,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	sourceID, err := h.sources.Create(context.Background(), domain.Source{
		Name:            "Saved Videos",
		CollectionType:  domain.CollectionSingles,
		MediaProfileID:  profileID,
		Enabled:         true,
		ShortsRule:      domain.InclusionInclude,
		LivestreamsRule: domain.InclusionInclude,
		CreatedAt:       h.now,
		UpdatedAt:       h.now,
	})
	if err != nil {
		t.Fatalf("seed singles source: %v", err)
	}
	mediaID := seedPendingMedia(t, h, sourceID)
	h.runner.result = ytdlp.DownloadResult{
		FilePath: filepath.Join(h.mediaDir, "Saved Videos", "Cool Video.mkv"),
		FileSize: 4242,
	}

	if err := h.svc.DownloadMedia(context.Background(), mediaID); err != nil {
		t.Fatalf("DownloadMedia() = %v", err)
	}
	if h.runner.artworkCalls != 0 {
		t.Errorf("artwork fetches = %d, want 0 for a source with no URL", h.runner.artworkCalls)
	}
}
