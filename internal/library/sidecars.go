package library

import (
	"context"
	"fmt"
	"log/slog"

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

	refreshed := 0
	wroteShow := false
	for _, media := range items {
		if media.Status != domain.MediaDownloaded || media.FilePath == "" {
			continue
		}
		changed, err := s.deps.Metadata.WriteFor(ctx, media.FilePath, media, source.Name, profile.MetadataFormat)
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
			s.writeShowMetadata(ctx, source, media.FilePath, profile)
			wroteShow = true
		}
	}
	return refreshed, nil
}
