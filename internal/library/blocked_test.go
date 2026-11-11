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

// firstAttempt and finalAttempt name which try of a standard three-attempt
// task budget a download is on, for tests whose outcome depends on it (and as
// the neutral default everywhere else).
var (
	firstAttempt  = jobs.Attempt{Number: 1, Budget: 3}
	secondAttempt = jobs.Attempt{Number: 2, Budget: 3}
	finalAttempt  = jobs.Attempt{Number: 3, Budget: 3}
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

// TestBlocked403SchedulesABoundedRetry pins the self-healing side of a 403
// block: while the task has budget left, the refusal costs an attempt and the
// next try is scheduled hours out — a client block usually clears on its own,
// and the standard seconds-to-minutes backoff would only collect the same
// refusal. The schedule starts quick and grows patient.
func TestBlocked403SchedulesABoundedRetry(t *testing.T) {
	tests := []struct {
		name    string
		attempt jobs.Attempt
		want    time.Duration
	}{
		{name: "first block comes back within the hour", attempt: firstAttempt, want: time.Hour},
		{name: "second block waits out most of a day", attempt: secondAttempt, want: 6 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			profileID := h.seedProfile(t)
			src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
			mediaID := seedPendingMedia(t, h, src.ID)
			h.runner.downloadErr = fmt.Errorf("%w: HTTP Error 403", ytdlp.ErrAccessDenied)

			err := h.svc.DownloadMedia(context.Background(), mediaID, tt.attempt)

			var retry *jobs.Retry
			if !errors.As(err, &retry) {
				t.Fatalf("DownloadMedia() = %v, want a jobs.Retry so the attempt is spent but rescheduled", err)
			}
			if want := h.now.Add(tt.want); !retry.RunAfter.Equal(want) {
				t.Errorf("retry run_after = %v, want %v", retry.RunAfter, want)
			}
			if !errors.Is(err, ytdlp.ErrAccessDenied) {
				t.Error("the retry must carry the 403 as its cause")
			}
			got, _ := h.media.Get(context.Background(), mediaID)
			if got.Status != domain.MediaFailed {
				t.Errorf("status = %q, want failed while waiting out the block", got.Status)
			}
			if got.LastError == "" {
				t.Error("expected the refusal recorded on the item")
			}
			// Escalation is saved for the final attempt: a block that heals
			// itself should never have paged anyone.
			if h.notifier.calls != 0 {
				t.Errorf("notifications = %d, want 0 before the budget is spent", h.notifier.calls)
			}
		})
	}
}

// TestBlocked403EscalatesOnceRetriesAreExhausted pins the bounded side: on the
// final attempt the task settles (nil error), the item stays failed with the
// reason, and the block is escalated to the notifier — automatic retries have
// limits, they do not circle forever.
func TestBlocked403EscalatesOnceRetriesAreExhausted(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID := seedPendingMedia(t, h, src.ID)
	h.runner.downloadErr = fmt.Errorf("%w: HTTP Error 403", ytdlp.ErrAccessDenied)

	err := h.svc.DownloadMedia(context.Background(), mediaID, finalAttempt)

	// A nil error settles the task: the budget is spent, so no further retry.
	if err != nil {
		t.Fatalf("DownloadMedia() = %v, want nil so the task settles", err)
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
		t.Errorf("notifications = %d, want the exhausted block escalated to the notifier", h.notifier.calls)
	}
}

// TestBlocked403WithUnknownBudgetFailsNow pins the zero-value guard: a caller
// that cannot say which attempt this is gets the old fail-fast behavior, not
// an open-ended retry loop.
func TestBlocked403WithUnknownBudgetFailsNow(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID := seedPendingMedia(t, h, src.ID)
	h.runner.downloadErr = fmt.Errorf("%w: HTTP Error 403", ytdlp.ErrAccessDenied)

	if err := h.svc.DownloadMedia(context.Background(), mediaID, jobs.Attempt{}); err != nil {
		t.Fatalf("DownloadMedia() = %v, want nil so the task settles", err)
	}
	if h.notifier.calls != 1 {
		t.Errorf("notifications = %d, want 1 (unknown budget escalates immediately)", h.notifier.calls)
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

	err := h.svc.DownloadMedia(context.Background(), mediaID, firstAttempt)

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

	err := h.svc.DownloadMedia(context.Background(), mediaID, firstAttempt)

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

	if err := h.svc.DownloadMedia(context.Background(), mediaID, firstAttempt); err != nil {
		t.Fatalf("DownloadMedia() = %v", err)
	}
	if h.runner.artworkCalls != 0 {
		t.Errorf("artwork fetches = %d, want 0 for a source with no URL", h.runner.artworkCalls)
	}
}
