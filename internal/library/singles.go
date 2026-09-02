package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// ErrNotAVideoURL reports that the pasted text is not recognisably a single
// YouTube video — it may be a channel, a playlist, or not a URL at all.
var ErrNotAVideoURL = errors.New("library: not a single-video URL")

// singlesSourceName is the display name of the app-managed bucket that holds
// one-off downloads.
const singlesSourceName = "One-off downloads"

// singlesIndexFrequency is stored on the singles source to satisfy the schema;
// it is never acted on, because the scheduler excludes the singles collection
// type from scanning entirely.
const singlesIndexFrequency = 24 * time.Hour

// DownloadVideo records one pasted video and queues its download at the
// front of the line, returning the media id so the caller can watch it.
//
// The video lands in an app-managed "One-off downloads" source, which gives it
// everything a tracked channel's videos get — naming template, sidecars,
// retries, the library screens — without inventing a parallel pipeline. A video
// already downloaded or already queued is simply returned; one that previously
// failed or was skipped is given a fresh attempt.
func (s *Service) DownloadVideo(ctx context.Context, rawURL string) (int64, error) {
	externalID, ok := domain.ParseWatchID(rawURL)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrNotAVideoURL, rawURL)
	}

	source, err := s.ensureSinglesSource(ctx)
	if err != nil {
		return 0, err
	}

	existing, found, err := s.deps.Media.FindBySource(ctx, source.ID, externalID)
	if err != nil {
		return 0, fmt.Errorf("check existing one-off: %w", err)
	}
	if found {
		return s.requeueSingle(ctx, existing)
	}

	now := s.deps.Clock.Now()
	media := domain.Media{
		SourceID:   source.ID,
		ExternalID: externalID,
		Status:     domain.MediaPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	id, err := s.deps.Media.Upsert(ctx, media)
	if err != nil {
		return 0, fmt.Errorf("record one-off video: %w", err)
	}
	if err := s.enqueueSingleDownload(ctx, source.ID, id, now); err != nil {
		return 0, err
	}
	slog.InfoContext(ctx, "queued a one-off video", "media_id", id, "external_id", externalID)
	return id, nil
}

// requeueSingle settles a repeat request for a video the bucket already knows:
// nothing to do when it is downloaded or already on its way, a fresh attempt
// when its last one failed or was skipped.
func (s *Service) requeueSingle(ctx context.Context, media domain.Media) (int64, error) {
	switch media.Status {
	case domain.MediaDownloaded, domain.MediaPending, domain.MediaDownloading:
		return media.ID, nil
	}
	now := s.deps.Clock.Now()
	if err := s.deps.Media.SetStatus(ctx, media.ID, domain.MediaPending, now); err != nil {
		return 0, fmt.Errorf("requeue one-off video: %w", err)
	}
	if err := s.enqueueSingleDownload(ctx, media.SourceID, media.ID, now); err != nil {
		return 0, err
	}
	return media.ID, nil
}

// enqueueSingleDownload queues a download for a one-off at scan-now priority —
// the user is watching for this one.
func (s *Service) enqueueSingleDownload(ctx context.Context, sourceID, mediaID int64, now time.Time) error {
	task := jobs.NewTask(jobs.TaskDownloadMedia, now).ForSource(sourceID).ForMedia(mediaID)
	task.Priority = scanNowPriority
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return fmt.Errorf("enqueue one-off download: %w", err)
	}
	return nil
}

// ensureSinglesSource finds the app-managed singles bucket, creating it on
// first use with the first profile — the seeded default unless the user
// removed it. The bucket is created through the repository, not AddSource,
// because it deliberately fails user-input validation (no URL, a collection
// type forms may not submit).
func (s *Service) ensureSinglesSource(ctx context.Context) (domain.Source, error) {
	sources, err := s.deps.Sources.List(ctx)
	if err != nil {
		return domain.Source{}, fmt.Errorf("list sources: %w", err)
	}
	for _, source := range sources {
		if source.CollectionType == domain.CollectionSingles {
			return source, nil
		}
	}

	profiles, err := s.deps.Profiles.List(ctx)
	if err != nil {
		return domain.Source{}, fmt.Errorf("list profiles: %w", err)
	}
	if len(profiles) == 0 {
		return domain.Source{}, errors.New("library: no media profile exists to download with")
	}

	now := s.deps.Clock.Now()
	source := domain.Source{
		Name:            singlesSourceName,
		CollectionType:  domain.CollectionSingles,
		MediaProfileID:  profiles[0].ID,
		IndexFrequency:  singlesIndexFrequency,
		CookieBehavior:  domain.CookieWhenNeeded,
		ShortsRule:      domain.InclusionInclude,
		LivestreamsRule: domain.InclusionInclude,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	id, err := s.deps.Sources.Create(ctx, source)
	if err != nil {
		return domain.Source{}, fmt.Errorf("create singles source: %w", err)
	}
	source.ID = id
	slog.InfoContext(ctx, "created the one-off downloads bucket", "source_id", id)
	return source, nil
}
