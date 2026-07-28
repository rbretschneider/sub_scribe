package library

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"sub_scribe/internal/domain"
)

// sidecarExtensions are the files sub_scribe writes beside a video. Only these
// are ever considered for removal — anything else in the folder belongs to
// someone else, whatever it looks like.
var sidecarExtensions = map[string]bool{
	".nfo": true,
	".jpg": true,
}

// sweepOrphanedSidecars deletes metadata files whose video is gone, returning how
// many were removed.
//
// They accumulate whenever a video leaves without its sidecars: a file deleted by
// hand, a retention pass, or a rename that only moved part of a set. What is left
// is metadata describing nothing, and a media server scanning the folder can pick
// it up and attach it to the wrong episode.
//
// The rules are deliberately narrow, because this deletes files. Only extensions
// sub_scribe writes are considered, only inside folders it has downloaded into,
// only when no video of any extension shares the name, and never the series-level
// tvshow.nfo, which belongs to the folder rather than to any one video.
func (s *Service) sweepOrphanedSidecars(ctx context.Context, sourceID int64) (int, error) {
	items, err := s.deps.Media.ListBySource(ctx, sourceID)
	if err != nil {
		return 0, nil
	}

	removed := 0
	for dir := range downloadDirs(items) {
		names := readFileNames(dir)
		stems := videoStems(names)
		for _, name := range names {
			if !isOrphanSidecar(name, stems) {
				continue
			}
			path := filepath.Join(dir, name)
			if err := os.Remove(path); err != nil {
				slog.WarnContext(ctx, "could not remove orphaned sidecar", "path", path, "error", err)
				continue
			}
			slog.InfoContext(ctx, "removed a sidecar whose video is gone", "path", path)
			removed++
		}
	}
	return removed, nil
}

// downloadDirs collects the folders a source's downloaded files live in, so the
// sweep only ever looks where sub_scribe has actually written.
func downloadDirs(items []domain.Media) map[string]bool {
	dirs := make(map[string]bool)
	for _, media := range items {
		if media.Status == domain.MediaDownloaded && media.FilePath != "" {
			dirs[filepath.Dir(media.FilePath)] = true
		}
	}
	return dirs
}

// videoStems returns the base names, without extension, of every file in the
// listing that is not itself a sidecar — that is, the videos.
func videoStems(names []string) map[string]bool {
	stems := make(map[string]bool)
	for _, name := range names {
		ext := strings.ToLower(filepath.Ext(name))
		if sidecarExtensions[ext] {
			continue
		}
		stems[strings.TrimSuffix(name, filepath.Ext(name))] = true
	}
	return stems
}

// isOrphanSidecar reports whether a file is a sidecar with no video to belong to.
//
// The stem is taken up to the FIRST dot after the video's name so that a
// multi-part suffix like ".en.srt" still resolves to its video; the check is a
// prefix match against known stems for the same reason.
func isOrphanSidecar(name string, stems map[string]bool) bool {
	if name == showNFOName {
		return false // belongs to the folder, not to any one video
	}
	if !sidecarExtensions[strings.ToLower(filepath.Ext(name))] {
		return false
	}
	for stem := range stems {
		if strings.HasPrefix(name, stem+".") {
			return false
		}
	}
	return true
}

// showNFOName is the series-level sidecar, which no video owns and which the
// sweep must therefore never treat as an orphan.
//
// It is spelled out here rather than shared with the metadata package that
// writes it, because that package imports this one for its port definitions and
// the dependency cannot run both ways. If the name ever changes, it changes in
// both places — and the sweep would start deleting the series file, so the test
// covering that case is the thing that catches it.
const showNFOName = "tvshow.nfo"
