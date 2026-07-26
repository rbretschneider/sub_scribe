package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/naming"
	"sub_scribe/internal/ytdlp"
)

// adoptableStatuses are the states a media item can be in while its file is
// nonetheless sitting on disk. Downloading is excluded because a worker owns it,
// and downloaded needs no adopting.
var adoptableStatuses = []domain.MediaStatus{
	domain.MediaSkipped,
	domain.MediaFailed,
	domain.MediaPending,
	domain.MediaUnavailable,
}

// adoptExistingFiles records media whose file is already on disk but which the
// database does not know about, returning how many were adopted.
//
// This exists because the archive on disk is the real artifact and the database
// is only a record of it: any disagreement should be resolved in favour of the
// files. It repairs items left mis-recorded by an earlier bug, and equally
// covers a restore from backup or a database rebuilt beside an existing library.
func (s *Service) adoptExistingFiles(ctx context.Context) (int, error) {
	resolver := s.newPathResolver()
	adopted := 0

	for _, status := range adoptableStatuses {
		items, err := s.deps.Media.ListByStatus(ctx, status, 0)
		if err != nil {
			return adopted, fmt.Errorf("list %s media: %w", status, err)
		}
		for _, media := range items {
			ok, err := s.adoptOne(ctx, resolver, media)
			if err != nil {
				return adopted, err
			}
			if ok {
				adopted++
			}
		}
	}
	return adopted, nil
}

// adoptOne records a single item as downloaded when its file is found on disk,
// reporting whether it did.
func (s *Service) adoptOne(ctx context.Context, resolver *pathResolver, media domain.Media) (bool, error) {
	base, ok := resolver.basePathFor(ctx, media)
	if !ok {
		return false, nil
	}
	path, ok := resolver.dirs.find(base)
	if !ok {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, nil
	}

	if err := s.deps.Media.MarkDownloaded(ctx, media.ID, path, info.Size(), s.deps.Clock.Now()); err != nil {
		return false, fmt.Errorf("adopt existing file for media %d: %w", media.ID, err)
	}
	slog.InfoContext(ctx, "adopted a file already on disk",
		"media_id", media.ID, "title", media.Metadata.Title, "path", path)
	return true, nil
}

// deleteSourceFiles removes the media files a source has downloaded, returning
// how many were deleted. Only paths sub_scribe recorded are touched — it never
// removes a directory wholesale — so files it did not put there are left alone.
// Directories emptied by the removal are then tidied up.
func (s *Service) deleteSourceFiles(ctx context.Context, sourceID int64) (int, error) {
	items, err := s.deps.Media.ListBySource(ctx, sourceID)
	if err != nil {
		return 0, fmt.Errorf("list media for deletion: %w", err)
	}

	removed := 0
	dirs := make(map[string]bool)
	for _, media := range items {
		if media.FilePath == "" {
			continue
		}
		if err := os.Remove(media.FilePath); err != nil {
			if !os.IsNotExist(err) {
				// One unremovable file must not abandon the rest half-deleted.
				slog.ErrorContext(ctx, "could not delete media file",
					"media_id", media.ID, "path", media.FilePath, "error", err)
			}
			continue
		}
		dirs[filepath.Dir(media.FilePath)] = true
		removed++
	}
	s.pruneEmptyDirs(ctx, dirs)
	return removed, nil
}

// pruneEmptyDirs removes directories left empty by a deletion, walking up toward
// the media root so an emptied "Channel/Season 2026" takes "Channel" with it. The
// media root itself is never removed.
func (s *Service) pruneEmptyDirs(ctx context.Context, dirs map[string]bool) {
	root := filepath.Clean(s.deps.MediaDir)
	for dir := range dirs {
		for current := filepath.Clean(dir); strings.HasPrefix(current, root) && current != root; {
			if err := os.Remove(current); err != nil {
				break // not empty, or not ours to remove
			}
			slog.DebugContext(ctx, "removed empty media directory", "path", current)
			current = filepath.Dir(current)
		}
	}
}

// directoryCache remembers each directory's listing so a pass over the whole
// archive reads a folder once rather than once per item. A year of one channel
// is thousands of items sharing a handful of folders, and the media volume is
// often a slow network or bind mount, so this is the difference between startup
// recovery taking seconds and taking minutes.
type directoryCache struct {
	listings map[string][]string
}

// newDirectoryCache builds an empty cache.
func newDirectoryCache() *directoryCache {
	return &directoryCache{listings: make(map[string][]string)}
}

// find returns the finished media file at basePath, reading its directory at
// most once for the lifetime of the cache.
func (c *directoryCache) find(basePath string) (string, bool) {
	dir := filepath.Dir(basePath)
	names, cached := c.listings[dir]
	if !cached {
		names = readFileNames(dir)
		c.listings[dir] = names
	}

	name, ok := ytdlp.MatchDownloadedFile(names, filepath.Base(basePath))
	if !ok {
		return "", false
	}
	return filepath.Join(dir, name), true
}

// readFileNames lists the file names in a directory, treating an unreadable or
// absent directory as empty — during recovery that simply means nothing to adopt.
func readFileNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// pathResolver renders the destination path for a media item, caching the source
// and profile lookups each one needs. Reconciliation walks every item in the
// archive, and without the cache that would be two repository reads per item.
type pathResolver struct {
	service  *Service
	sources  map[int64]domain.Source
	profiles map[int64]domain.MediaProfile
	// missing records lookups already known to fail, so a broken reference is not
	// re-queried for every item that shares it.
	missing map[int64]bool
	// dirs caches directory listings for the same reason.
	dirs *directoryCache
}

// newPathResolver builds an empty resolver bound to the service.
func (s *Service) newPathResolver() *pathResolver {
	return &pathResolver{
		service:  s,
		sources:  make(map[int64]domain.Source),
		profiles: make(map[int64]domain.MediaProfile),
		missing:  make(map[int64]bool),
		dirs:     newDirectoryCache(),
	}
}

// basePathFor returns the absolute path a media item's file would occupy, without
// its extension. It reports false when the source, profile, or template cannot be
// resolved — a missing reference is not worth failing recovery over.
func (r *pathResolver) basePathFor(ctx context.Context, media domain.Media) (string, bool) {
	source, profile, ok := r.contextFor(ctx, media.SourceID)
	if !ok {
		return "", false
	}
	rendered, err := r.service.deps.Naming.Render(
		profile.OutputPathTemplate, naming.NewContext(source.Name, media))
	if err != nil {
		return "", false
	}
	return filepath.Join(r.service.deps.MediaDir, filepath.FromSlash(rendered)), true
}

// contextFor returns the source and profile for a source id, loading and caching
// them on first use.
func (r *pathResolver) contextFor(ctx context.Context, sourceID int64) (domain.Source, domain.MediaProfile, bool) {
	if r.missing[sourceID] {
		return domain.Source{}, domain.MediaProfile{}, false
	}

	source, ok := r.sources[sourceID]
	if !ok {
		loaded, err := r.service.deps.Sources.Get(ctx, sourceID)
		if err != nil {
			r.missing[sourceID] = true
			return domain.Source{}, domain.MediaProfile{}, false
		}
		source = loaded
		r.sources[sourceID] = source
	}

	profile, ok := r.profiles[source.MediaProfileID]
	if !ok {
		loaded, err := r.service.deps.Profiles.Get(ctx, source.MediaProfileID)
		if err != nil {
			r.missing[sourceID] = true
			return domain.Source{}, domain.MediaProfile{}, false
		}
		profile = loaded
		r.profiles[source.MediaProfileID] = profile
	}
	return source, profile, true
}
