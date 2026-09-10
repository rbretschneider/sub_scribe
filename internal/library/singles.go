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
// The video lands in an app-managed "One-off downloads" source for the chosen
// profile, which gives it everything a tracked channel's videos get — the
// profile's naming template, destination folder, sidecar format, retries, the
// library screens — without inventing a parallel pipeline. That profile choice
// is what routes an IMDb-documented find into a Plex movie library while
// ordinary saves stay in the YouTube archive. A zero profileID uses the first
// (default) profile. A video already downloaded or already queued in that
// bucket is simply returned; one that previously failed or was skipped is
// given a fresh attempt.
func (s *Service) DownloadVideo(ctx context.Context, rawURL string, profileID int64) (int64, error) {
	externalID, ok := domain.ParseWatchID(rawURL)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrNotAVideoURL, rawURL)
	}

	source, err := s.ensureSinglesSource(ctx, profileID)
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

// ensureSinglesSource finds the app-managed singles bucket for a profile,
// creating it on first use. Each profile gets its own bucket because the
// profile is what decides where and how a one-off is filed — a movie routed to
// a Plex library must not share a source with saves bound for the YouTube
// archive. A zero profileID means the first (default) profile. The bucket is
// created through the repository, not AddSource, because it deliberately fails
// user-input validation (no URL, a collection type forms may not submit).
func (s *Service) ensureSinglesSource(ctx context.Context, profileID int64) (domain.Source, error) {
	profile, err := s.singlesProfile(ctx, profileID)
	if err != nil {
		return domain.Source{}, err
	}

	sources, err := s.deps.Sources.List(ctx)
	if err != nil {
		return domain.Source{}, fmt.Errorf("list sources: %w", err)
	}
	for _, source := range sources {
		if source.CollectionType == domain.CollectionSingles && source.MediaProfileID == profile.ID {
			return source, nil
		}
	}

	now := s.deps.Clock.Now()
	source := domain.Source{
		Name:            singlesBucketName(ctx, s, profile),
		CollectionType:  domain.CollectionSingles,
		MediaProfileID:  profile.ID,
		IndexFrequency:  singlesIndexFrequency,
		CookieBehavior:  domain.CookieWhenNeeded,
		ShortsRule:      domain.InclusionInclude,
		LivestreamsRule: domain.InclusionInclude,
		Enabled:         true,
		FeedToken:       domain.NewFeedToken(),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	id, err := s.deps.Sources.Create(ctx, source)
	if err != nil {
		return domain.Source{}, fmt.Errorf("create singles source: %w", err)
	}
	source.ID = id
	slog.InfoContext(ctx, "created a one-off downloads bucket",
		"source_id", id, "profile", profile.Name)
	return source, nil
}

// singlesProfile resolves which profile a one-off download uses: the requested
// one, or the first (default) profile when none was chosen.
func (s *Service) singlesProfile(ctx context.Context, profileID int64) (domain.MediaProfile, error) {
	if profileID > 0 {
		profile, err := s.deps.Profiles.Get(ctx, profileID)
		if err != nil {
			return domain.MediaProfile{}, fmt.Errorf("get profile %d: %w", profileID, err)
		}
		return profile, nil
	}
	profiles, err := s.deps.Profiles.List(ctx)
	if err != nil {
		return domain.MediaProfile{}, fmt.Errorf("list profiles: %w", err)
	}
	if len(profiles) == 0 {
		return domain.MediaProfile{}, errors.New("library: no media profile exists to download with")
	}
	return profiles[0], nil
}

// singlesBucketName names a profile's one-off bucket. The default profile keeps
// the plain name every existing install already has; other profiles get the
// profile's name appended so the sources list tells the buckets apart.
func singlesBucketName(ctx context.Context, s *Service, profile domain.MediaProfile) string {
	profiles, err := s.deps.Profiles.List(ctx)
	if err == nil && len(profiles) > 0 && profiles[0].ID == profile.ID {
		return singlesSourceName
	}
	return singlesSourceName + " (" + profile.Name + ")"
}
