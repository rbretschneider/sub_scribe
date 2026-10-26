package library

import (
	"context"
	"fmt"
	"log/slog"

	"sub_scribe/internal/descclean"
	"sub_scribe/internal/domain"
)

// RefreshSidecars rewrites the metadata files beside every downloaded item, and
// the series-level file at each channel's root, returning how many it changed.
//
// This runs unprompted at startup because the alternative does not work. A
// sidecar written by an older version is missing fields a media server needs,
// and nothing about the library looks wrong: the videos play, the files are
// where they should be, and the server just quietly shows the wrong titles. The
// user has no way to know that the fix is to re-download everything, and no
// reason to guess that a button somewhere would rewrite files they cannot see.
//
// It is safe to run unattended precisely because it does not move or delete
// anything — it only rewrites sidecars sub_scribe wrote in the first place, and
// only when their content would actually change. Renaming media files stays
// behind an explicit action, since that is not something to do to someone's
// archive without being asked.
//
// It is deliberately not part of startup recovery. Every downloaded item costs a
// read to find out whether anything changed, and on a large archive over a slow
// mount that is minutes of work — far too long to hold the UI closed for a
// repair that almost always finds nothing to do.
func (s *Service) RefreshSidecars(ctx context.Context) (int, error) {
	sources, err := s.deps.Sources.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list sources for sidecar refresh: %w", err)
	}

	refreshed := 0
	for _, source := range sources {
		count, err := s.refreshSourceSidecars(ctx, source)
		if err != nil {
			return refreshed, err
		}
		refreshed += count
	}
	return refreshed, nil
}

// refreshFeed regenerates a source's feed so a change to description cleanup
// reaches it too. A source with nothing downloaded has no feed worth writing
// unless cleanup is on. The feed writer skips an unchanged feed, so for a source
// with cleanup off this costs a read and no write.
func (s *Service) refreshFeed(ctx context.Context, source domain.Source, items []domain.Media, downloaded int, budget *descclean.Budget) {
	if downloaded == 0 && !source.CleansDescriptions() {
		return
	}
	if err := s.deps.Feed.WriteFeed(ctx, source, s.forOutputBatch(ctx, source, items, budget)); err != nil {
		slog.WarnContext(ctx, "could not refresh feed", "source_id", source.ID, "error", err)
	}
}

// RefreshSourceSidecars rewrites one source's metadata files and feed from its
// current settings and returns how many metadata files changed. The count leaves
// the feed out because the feed port does not report whether it wrote.
func (s *Service) RefreshSourceSidecars(ctx context.Context, sourceID int64) (int, error) {
	source, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return 0, fmt.Errorf("get source for sidecar refresh: %w", err)
	}
	return s.refreshSourceSidecars(ctx, source)
}

// refreshSourceSidecars rewrites one source's sidecars, reporting how many
// changed. A source whose profile has gone missing is skipped rather than
// failing the whole pass: recovery should repair what it can.
func (s *Service) refreshSourceSidecars(ctx context.Context, source domain.Source) (int, error) {
	profile, err := s.deps.Profiles.Get(ctx, source.MediaProfileID)
	if err != nil {
		return 0, nil
	}
	items, err := s.deps.Media.ListBySource(ctx, source.ID)
	if err != nil {
		return 0, fmt.Errorf("list media for sidecar refresh: %w", err)
	}

	// One time budget covers the whole run: the sidecars and then the feed.
	budget := s.newBudget()
	refreshed := 0
	wroteShow := false
	downloaded := 0
	for _, media := range items {
		if media.Status != domain.MediaDownloaded || media.FilePath == "" {
			continue
		}
		downloaded++
		changed, err := s.deps.Metadata.WriteFor(ctx, media.FilePath, s.forOutput(ctx, source, media, budget), source.Name, profile.MetadataFormat)
		if err != nil {
			slog.WarnContext(ctx, "could not refresh metadata sidecar",
				"media_id", media.ID, "path", media.FilePath, "error", err)
			continue
		}
		if changed {
			refreshed++
		}
		if !wroteShow {
			// Every item shares one channel folder, so the series file is written
			// once, from the first item that tells us where that folder is.
			s.writeShowSidecars(ctx, source, media.FilePath, profile)
			wroteShow = true
		}
	}
	s.refreshFeed(ctx, source, items, downloaded, budget)
	return refreshed, nil
}
