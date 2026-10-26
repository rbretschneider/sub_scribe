package library

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"sub_scribe/internal/descclean"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// Description cleanup happens only at output time, on a copy.
//
// forOutput returns the media value the writers should be given: the original
// untouched when cleanup is off, or a copy whose description has been cleaned.
// The copy is a local value handed to a writer and then dropped. Never pass it to
// a repository method, or the stored original would be overwritten with the
// cleaned text and turning cleanup off could no longer restore it.
//
// Code that changes stored descriptions must call invalidateChannel for the
// uploader, so channel statistics are rebuilt rather than served stale. (The
// cache also re-checks a row count on every use, which covers most drift.)

// Default time allowances; see Deps.BatchBudget and Deps.PreviewBudget.
const (
	defaultBatchBudget   = descclean.DefaultBatchBudget
	defaultPreviewBudget = 500 * time.Millisecond
)

func (s *Service) newBudget() *descclean.Budget {
	total := s.deps.BatchBudget
	if total <= 0 {
		total = defaultBatchBudget
	}
	return descclean.NewBudget(total)
}

func (s *Service) invalidateChannel(uploader string) {
	if s.deps.Cleaner != nil {
		s.deps.Cleaner.Invalidate(uploader)
	}
}

// forOutput returns media as it should be written out. It never fails: any
// error, panic or exhausted budget is logged (by class only, never text) and the
// original is returned, so cleanup cannot fail a download, index or sidecar job.
// A nil budget means a fresh single-item batch.
func (s *Service) forOutput(ctx context.Context, source domain.Source, media domain.Media, b *descclean.Budget) domain.Media {
	// Check the mode before any other work so an Off source costs nothing.
	if s.deps.Cleaner == nil || !source.CleansDescriptions() || media.Metadata.Description == "" {
		return media
	}
	if b == nil {
		b = s.newBudget()
	}
	res, ok := s.cleanSafely(ctx, source, media, b)
	if !ok {
		return media
	}
	out := media
	out.Metadata.Description = res.Cleaned
	return out
}

// forOutputBatch is forOutput for a list sharing one time budget (a feed
// regeneration). Only downloaded items are cleaned: they are the only ones a feed
// shows. With cleanup off the input slice is returned as it is.
func (s *Service) forOutputBatch(ctx context.Context, source domain.Source, items []domain.Media, b *descclean.Budget) []domain.Media {
	if s.deps.Cleaner == nil || !source.CleansDescriptions() {
		return items
	}
	if b == nil {
		b = s.newBudget()
	}
	out := make([]domain.Media, len(items))
	for i, m := range items {
		out[i] = m
		if m.Status == domain.MediaDownloaded {
			out[i] = s.forOutput(ctx, source, m, b)
		}
	}
	return out
}

// cleanSafely runs the cleaner, converting an error or panic into a logged
// fallback.
func (s *Service) cleanSafely(ctx context.Context, source domain.Source, media domain.Media, b *descclean.Budget) (res descclean.Result, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.warnOnce(ctx, b, source, media, descclean.ClassPanic)
			res, ok = descclean.Result{}, false
		}
	}()
	res, err := s.clean(ctx, source, media, b)
	if err != nil {
		s.warnOnce(ctx, b, source, media, descclean.ErrorClass(err))
		return descclean.Result{}, false
	}
	if res.PatternsRejected {
		s.warnOnce(ctx, b, source, media, descclean.ClassPatternRejected)
	}
	if res.UserPatternsSkipped || res.PatternInputCapped {
		s.warnOnce(ctx, b, source, media, descclean.ClassTimeout)
	}
	return res, true
}

// clean gathers the channel history and runs the cleaner, whatever the source's
// setting says; callers decide whether the result is applied.
func (s *Service) clean(ctx context.Context, source domain.Source, media domain.Media, b *descclean.Budget) (descclean.Result, error) {
	stats, err := s.deps.Cleaner.Stats(ctx, media.Metadata.Uploader, s.history())
	if err != nil {
		// Losing history only loses the repetition layer; the others still apply.
		s.warnOnce(ctx, b, source, media, descclean.ClassHistory)
		stats = nil
	}
	return s.deps.Cleaner.Clean(ctx, descclean.Input{
		Description: media.Metadata.Description,
		SourceID:    source.ID,
		Mode:        string(source.DescriptionCleanup.Effective()),
		Patterns:    source.DescriptionStripPatterns,
		Stats:       stats,
	}, b)
}

// history returns the optional history port as the cleaner's interface, keeping a
// nil port a true nil (a typed nil inside an interface would not be).
func (s *Service) history() descclean.History {
	if s.deps.History == nil {
		return nil
	}
	return s.deps.History
}

// warnOnce logs a cleanup problem at most once per (source, class) per batch. It
// carries identifiers and a fixed class only: never description text, paragraph
// text or a pattern.
func (s *Service) warnOnce(ctx context.Context, b *descclean.Budget, source domain.Source, media domain.Media, class string) {
	if b != nil && !b.FirstWarning(source.ID, class) {
		return
	}
	slog.WarnContext(ctx, "description cleanup skipped; using the original description",
		"source_id", source.ID, "media_id", media.ID, "class", class,
		"patterns", len(source.DescriptionStripPatterns))
}

// descriptionSettingsChanged reports whether an edit altered what cleanup
// writes, which is what makes existing sidecars and the feed stale.
func descriptionSettingsChanged(before, after domain.Source) bool {
	return before.DescriptionCleanup.Effective() != after.DescriptionCleanup.Effective() ||
		!slices.Equal(before.DescriptionStripPatterns, after.DescriptionStripPatterns)
}

// requestSidecarRefresh queues a refresh of a source's sidecars and feed. A
// failure to queue is logged, not returned: the save already succeeded and the
// startup pass catches up.
func (s *Service) requestSidecarRefresh(ctx context.Context, sourceID int64) {
	task := jobs.NewTask(jobs.TaskRefreshSidecars, s.deps.Clock.Now()).ForSource(sourceID)
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		slog.WarnContext(ctx, "could not queue a sidecar refresh", "source_id", sourceID, "error", err)
	}
}

// PreviewDescription shows what cleanup would do to one item's description,
// using the source's real patterns whether or not cleanup is switched on. It runs
// on a web request, so it has its own short deadline; if the work cannot finish
// in time the preview is marked unavailable rather than failing the page.
func (s *Service) PreviewDescription(ctx context.Context, mediaID int64) (DescriptionPreview, error) {
	media, err := s.deps.Media.Get(ctx, mediaID)
	if err != nil {
		return DescriptionPreview{}, fmt.Errorf("get media: %w", err)
	}
	source, err := s.deps.Sources.Get(ctx, media.SourceID)
	if err != nil {
		return DescriptionPreview{}, fmt.Errorf("get source: %w", err)
	}
	original := media.Metadata.Description
	if s.deps.Cleaner == nil || strings.TrimSpace(original) == "" {
		return DescriptionPreview{Original: original, SourceID: source.ID}, nil
	}

	preview := DescriptionPreview{
		Available: true,
		Enabled:   source.CleansDescriptions(),
		Original:  original,
		SourceID:  source.ID,
	}
	total := s.deps.PreviewBudget
	if total <= 0 {
		total = defaultPreviewBudget
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	res, ok := s.previewSafely(ctx, source, media, descclean.NewBudget(total))
	if !ok {
		preview.Unavailable = true
		preview.UnavailableReason = "the description could not be analysed in time"
		return preview, nil
	}
	if res.UserPatternsSkipped {
		preview.Unavailable = true
		preview.UnavailableReason = "your patterns took too long to run"
		return preview, nil
	}
	preview.Cleaned = res.Cleaned
	preview.FellBack = res.FellBack
	preview.HistorySize = res.HistorySize
	preview.PatternsIgnored = res.PatternsRejected
	for _, r := range res.Removed {
		preview.Removed = append(preview.Removed, RemovedParagraph{
			Text: r.Text, Reason: string(r.Reason), Label: r.Label(),
		})
	}
	return preview, nil
}

// previewSafely is cleanSafely for the request path: same recovery, with the
// failure reported to the caller instead of a log line per class.
func (s *Service) previewSafely(ctx context.Context, source domain.Source, media domain.Media, b *descclean.Budget) (res descclean.Result, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.warnOnce(ctx, b, source, media, descclean.ClassPanic)
			res, ok = descclean.Result{}, false
		}
	}()
	res, err := s.clean(ctx, source, media, b)
	if err != nil {
		s.warnOnce(ctx, b, source, media, descclean.ErrorClass(err))
		return descclean.Result{}, false
	}
	return res, true
}

var _ DescriptionPreviewer = (*Service)(nil)
var _ SidecarRefresher = (*Service)(nil)
