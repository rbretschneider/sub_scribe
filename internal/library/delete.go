package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"sub_scribe/internal/domain"
)

// ErrMediaInFlight reports a deletion refused because the item is queued or
// actively downloading — deleting under a running download would race the
// worker and leave the two disagreeing about what exists.
var ErrMediaInFlight = fmt.Errorf("library: media is queued or downloading; wait for it to settle or pause the source first")

// DeleteMedia removes one item's downloaded file and its sidecars from disk and
// tombstones the record, so the video is gone from the library and from the
// media server after its next refresh — and a rescan that rediscovers it never
// downloads it again. This is the per-video counterpart to DeleteSource's
// delete-files option, for surgically removing individual items (a junk clip
// in an otherwise wanted source) without touching their neighbours.
func (s *Service) DeleteMedia(ctx context.Context, id int64) error {
	media, err := s.deps.Media.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get media: %w", err)
	}
	if media.Status == domain.MediaPending || media.Status == domain.MediaDownloading {
		return ErrMediaInFlight
	}

	if media.FilePath != "" {
		if err := removeFile(media.FilePath); err != nil {
			return fmt.Errorf("delete media file %q: %w", media.FilePath, err)
		}
		removeSidecarsFor(ctx, media.FilePath)
		s.pruneEmptyDirs(ctx, map[string]bool{filepath.Dir(media.FilePath): true})
	}

	if err := s.deps.Media.MarkDeleted(ctx, id, s.deps.Clock.Now()); err != nil {
		return fmt.Errorf("tombstone media %d: %w", id, err)
	}
	slog.InfoContext(ctx, "deleted a video on request",
		"media_id", id, "title", media.Metadata.Title, "file", media.FilePath)
	return nil
}

// removeSidecarsFor deletes the sidecar files belonging to a media file: those
// in its directory whose name is the file's stem followed by a dot, with an
// extension sub_scribe writes. The stem-dot rule is the same one renaming uses,
// so "Video [x].nfo", "Video [x].jpg", and "Video [x].en.srt" all go while
// "Video [x] 2.mkv" and its set stay. Failures are logged, not fatal — the
// orphan sweep exists to catch stragglers.
func removeSidecarsFor(ctx context.Context, mediaPath string) {
	dir := filepath.Dir(mediaPath)
	stem := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	for _, name := range readFileNames(dir) {
		if !strings.HasPrefix(name, stem+".") || !sidecarExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "could not remove a deleted video's sidecar", "path", path, "error", err)
		}
	}
}
