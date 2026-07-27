package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// RenameReport summarises a naming pass over a source's existing files.
type RenameReport struct {
	// Checked is how many downloaded files were examined.
	Checked int
	// Renamed is how many were moved to the layout the template now describes.
	Renamed int
	// Blocked is how many were left alone because something already occupied
	// their destination. Overwriting it could destroy a file, so they are
	// reported instead.
	Blocked int
}

// ApplyNamingTemplate moves a source's already-downloaded files to the paths its
// current naming template describes, and returns what it did.
//
// Changing a template only affects what is downloaded next, so any library that
// has outlived a template change is split between two layouts. That is not a
// cosmetic problem: media servers read the layout to identify the content, so a
// file left in the old shape is a file the server describes wrongly — Plex
// invents titles like "Episode 04-22" for anything it cannot parse — and no
// amount of rescanning fixes it, because the filename is the input.
//
// Sidecars move with their media file. A thumbnail or .nfo left behind under the
// old name is orphaned metadata that the server may then attach to nothing.
func (s *Service) ApplyNamingTemplate(ctx context.Context, sourceID int64) (RenameReport, error) {
	items, err := s.deps.Media.ListBySource(ctx, sourceID)
	if err != nil {
		return RenameReport{}, fmt.Errorf("list media for renaming: %w", err)
	}

	resolver := s.newPathResolver()
	vacated := make(map[string]bool)
	var report RenameReport

	for _, media := range items {
		if media.Status != domain.MediaDownloaded || media.FilePath == "" {
			continue
		}
		report.Checked++

		want, ok := s.wantedPathFor(ctx, resolver, media)
		if !ok || want == media.FilePath {
			continue
		}
		if _, err := os.Stat(media.FilePath); err != nil {
			// The recorded file is gone. That is a different inconsistency, and
			// startup reconciliation owns it; reporting it as blocked here would
			// point the user at a name clash that does not exist.
			continue
		}
		moved, err := s.moveMediaFile(ctx, media, want)
		if err != nil {
			return report, err
		}
		if !moved {
			report.Blocked++
			continue
		}
		vacated[filepath.Dir(media.FilePath)] = true
		report.Renamed++
	}

	s.pruneEmptyDirs(ctx, vacated)
	return report, nil
}

// wantedPathFor renders where a media item's file belongs under the current
// template, keeping the extension it already has — the template describes the
// name, never the container format.
func (s *Service) wantedPathFor(ctx context.Context, resolver *pathResolver, media domain.Media) (string, bool) {
	base, ok := resolver.basePathFor(ctx, media)
	if !ok {
		return "", false
	}
	return base + filepath.Ext(media.FilePath), true
}

// moveMediaFile renames one media file and its sidecars, recording the new path.
// It reports false when the destination is already taken, which is left for the
// caller to count rather than resolved by overwriting.
func (s *Service) moveMediaFile(ctx context.Context, media domain.Media, want string) (bool, error) {
	if _, err := os.Stat(want); err == nil {
		slog.WarnContext(ctx, "not renaming: something is already at the destination",
			"media_id", media.ID, "from", media.FilePath, "to", want)
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		return false, fmt.Errorf("create directory for renamed media %d: %w", media.ID, err)
	}

	for _, move := range companionMoves(media.FilePath, want) {
		if err := os.Rename(move.from, move.to); err != nil {
			// A sidecar that will not move is worth a line in the log, but it must
			// not abandon the media file where a server cannot identify it.
			slog.WarnContext(ctx, "could not move a file alongside the media",
				"media_id", media.ID, "from", move.from, "to", move.to, "error", err)
			continue
		}
		if move.from == media.FilePath {
			media.FilePath = move.to
		}
	}
	if media.FilePath != want {
		return false, fmt.Errorf("rename media %d: the media file itself did not move", media.ID)
	}

	media.UpdatedAt = s.deps.Clock.Now()
	if _, err := s.deps.Media.Upsert(ctx, media); err != nil {
		return false, fmt.Errorf("record renamed path for media %d: %w", media.ID, err)
	}
	slog.InfoContext(ctx, "renamed to match the naming template",
		"media_id", media.ID, "path", want)
	return true, nil
}

// fileMove is one from/to pair in a rename.
type fileMove struct{ from, to string }

// companionMoves lists the media file and every sidecar sharing its name, paired
// with where each one is going. Matching requires a dot straight after the stem
// so that "Video [abc].mkv" claims "Video [abc].jpg" and "Video [abc].en.srt"
// without also claiming a different video whose name merely starts the same way.
func companionMoves(from, to string) []fileMove {
	dir := filepath.Dir(from)
	stem := strings.TrimSuffix(filepath.Base(from), filepath.Ext(from))
	newStem := strings.TrimSuffix(filepath.Base(to), filepath.Ext(to))
	newDir := filepath.Dir(to)

	moves := make([]fileMove, 0, 3)
	for _, name := range readFileNames(dir) {
		if !strings.HasPrefix(name, stem+".") {
			continue
		}
		suffix := strings.TrimPrefix(name, stem)
		moves = append(moves, fileMove{
			from: filepath.Join(dir, name),
			to:   filepath.Join(newDir, newStem+suffix),
		})
	}
	return moves
}

// RequestRename enqueues a naming pass over a source's existing files. It
// confirms the source exists first so a bad id fails loudly rather than queuing
// dead work.
func (s *Service) RequestRename(ctx context.Context, id int64) error {
	if _, err := s.deps.Sources.Get(ctx, id); err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	task := jobs.NewTask(jobs.TaskRenameFiles, s.deps.Clock.Now()).ForSource(id)
	task.Priority = scanNowPriority
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return fmt.Errorf("enqueue rename: %w", err)
	}
	return nil
}
