package library

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/events"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/naming"
	"sub_scribe/internal/ytdlp"
)

// scanNowPriority ranks a user-requested scan above routine scheduled work so it
// runs promptly.
const scanNowPriority = 10

// defaultIndexFrequency is applied when a source is created without an explicit
// re-scan cadence, so every source is polled on a sensible schedule.
const defaultIndexFrequency = 6 * time.Hour

// errURLRequired is returned by AddSource/UpdateSource when the URL is blank. The
// name may be blank: it is auto-filled from the channel name on first index.
var errURLRequired = errors.New("source url must not be empty")

// Deps bundles the collaborators the Service needs. Every field is an interface
// (or a stateless value type) so the core depends on abstractions and each
// collaborator can be replaced with a fake in tests.
type Deps struct {
	Sources      SourceRepo
	Media        MediaRepo
	Profiles     ProfileRepo
	Tasks        TaskEnqueuer
	Queue        QueueMaintain
	Runner       ytdlp.Runner
	Naming       *naming.Renderer
	Metadata     MetadataWriter
	Artwork      ArtworkWriter
	Feed         FeedWriter
	Notifier     Notifier
	SponsorBlock SponsorBlockBuilder
	Hook         PostDownloadHook
	Events       events.Publisher
	Clock        jobs.Clock
	// DownloadPace rations how often a download may start. Nil means unpaced,
	// which is what tests and a deliberately disabled configuration both want.
	DownloadPace DownloadPacer
	MediaDir     string
	// TempDir is handed to yt-dlp as its scratch space so partial downloads never
	// touch MediaDir. Empty leaves yt-dlp's default in place.
	TempDir     string
	CookiesPath string
	// JobRetention is how long finished queue entries are kept. Zero keeps them
	// forever.
	JobRetention time.Duration
}

// Service is sub_scribe's application core. It coordinates repositories, yt-dlp,
// naming, metadata, and feeds to satisfy the user-facing and background-task
// interfaces, blocking on no slow work in the request path.
type Service struct {
	deps Deps
}

// NewService constructs a Service from its dependencies.
func NewService(deps Deps) *Service {
	return &Service{deps: deps}
}

// Compile-time assertions that Service implements every client interface.
var (
	_ SourceService  = (*Service)(nil)
	_ ProfileService = (*Service)(nil)
	_ Indexer        = (*Service)(nil)
	_ Downloader     = (*Service)(nil)
	_ Retainer       = (*Service)(nil)
	_ Redownloader   = (*Service)(nil)
	_ LibraryReader  = (*Service)(nil)
)

// AddSource validates the input, persists a new enabled source, and enqueues a
// single index task for it. It returns immediately without invoking yt-dlp, so
// adding a source is instant regardless of how large its collection is.
func (s *Service) AddSource(ctx context.Context, input AddSourceInput) (domain.Source, error) {
	normalized, err := s.normalizeAndValidate(ctx, input)
	if err != nil {
		return domain.Source{}, err
	}

	now := s.deps.Clock.Now()
	source := sourceFromInput(normalized, now)
	source.Enabled = true

	id, err := s.deps.Sources.Create(ctx, source)
	if err != nil {
		return domain.Source{}, fmt.Errorf("create source: %w", err)
	}
	source.ID = id

	task := jobs.NewTask(jobs.TaskIndexSource, now).ForSource(id)
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return domain.Source{}, fmt.Errorf("enqueue index task: %w", err)
	}
	return source, nil
}

// GetSource returns a single source by id.
func (s *Service) GetSource(ctx context.Context, id int64) (domain.Source, error) {
	return s.deps.Sources.Get(ctx, id)
}

// ListSources returns all tracked sources.
func (s *Service) ListSources(ctx context.Context) ([]domain.Source, error) {
	return s.deps.Sources.List(ctx)
}

// UpdateSource revalidates the input and persists the changes, preserving the
// source's identity, enabled state, and indexing history.
func (s *Service) UpdateSource(ctx context.Context, id int64, input AddSourceInput) (domain.Source, error) {
	existing, err := s.deps.Sources.Get(ctx, id)
	if err != nil {
		return domain.Source{}, fmt.Errorf("get source: %w", err)
	}

	normalized, err := s.normalizeAndValidate(ctx, input)
	if err != nil {
		return domain.Source{}, err
	}

	now := s.deps.Clock.Now()
	source := sourceFromInput(normalized, now)
	source.ID = existing.ID
	source.Enabled = existing.Enabled
	source.LastIndexedAt = existing.LastIndexedAt
	source.CreatedAt = existing.CreatedAt

	if err := s.deps.Sources.Update(ctx, source); err != nil {
		return domain.Source{}, fmt.Errorf("update source: %w", err)
	}
	return source, nil
}

// SetSourceEnabled pauses or resumes a source. A paused source is skipped by the
// scheduler, so no new scans are started and nothing new is discovered — but its
// settings and everything already downloaded are left untouched, which is what
// makes pausing a safe alternative to deleting.
func (s *Service) SetSourceEnabled(ctx context.Context, id int64, enabled bool) error {
	source, err := s.deps.Sources.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if source.Enabled == enabled {
		return nil
	}

	source.Enabled = enabled
	source.UpdatedAt = s.deps.Clock.Now()
	if err := s.deps.Sources.Update(ctx, source); err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	slog.InfoContext(ctx, "source availability changed",
		"source_id", id, "name", source.Name, "enabled", enabled)
	return nil
}

// DeleteSource removes a source. By default only sub_scribe's records go: the
// downloaded videos stay on disk, so the decision is reversible and re-adding the
// source adopts the existing files. When opts.DeleteFiles is set the media files
// are deleted too, which is not reversible.
//
// Files are removed before the source, because deleting the source cascades its
// media rows away and those rows are the only record of where the files live.
func (s *Service) DeleteSource(ctx context.Context, id int64, opts DeleteSourceOptions) error {
	if opts.DeleteFiles {
		removed, err := s.deleteSourceFiles(ctx, id)
		if err != nil {
			return err
		}
		slog.WarnContext(ctx, "deleted downloaded files with source", "source_id", id, "files", removed)
	}
	return s.deps.Sources.Delete(ctx, id)
}

// RequestScan enqueues an immediate, high-priority index task for a source so a
// user can force a scan without waiting for the schedule. It confirms the source
// exists first so a bad id fails loudly instead of queuing dead work.
func (s *Service) RequestScan(ctx context.Context, id int64) error {
	if _, err := s.deps.Sources.Get(ctx, id); err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	task := jobs.NewTask(jobs.TaskIndexSource, s.deps.Clock.Now()).ForSource(id)
	task.Priority = scanNowPriority
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return fmt.Errorf("enqueue scan: %w", err)
	}
	return nil
}

// sourceFromInput maps validated input onto a domain.Source with fresh
// timestamps. Callers set ID and lifecycle fields (Enabled, LastIndexedAt).
func sourceFromInput(input AddSourceInput, now time.Time) domain.Source {
	return domain.Source{
		Name:               input.Name,
		URL:                input.URL,
		CollectionType:     input.CollectionType,
		MediaProfileID:     input.MediaProfileID,
		IndexFrequency:     input.IndexFrequency,
		CookieBehavior:     input.CookieBehavior,
		DownloadCutoff:     input.DownloadCutoff,
		CutoffWindow:       input.CutoffWindow,
		TitleFilterPattern: input.TitleFilterPattern,
		ShortsRule:         input.ShortsRule,
		LivestreamsRule:    input.LivestreamsRule,
		RetentionAfter:     input.RetentionAfter,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

// normalizeAndValidate applies defaults and rejects malformed input. It returns
// a normalized copy leaving the caller's value untouched.
func (s *Service) normalizeAndValidate(ctx context.Context, input AddSourceInput) (AddSourceInput, error) {
	// A blank name is allowed: it is filled from the channel name on first index.
	input.Name = strings.TrimSpace(input.Name)
	if strings.TrimSpace(input.URL) == "" {
		return input, errURLRequired
	}
	if !input.CollectionType.IsValid() {
		return input, fmt.Errorf("invalid collection type %q", input.CollectionType)
	}
	if !input.CookieBehavior.IsValid() {
		return input, fmt.Errorf("invalid cookie behavior %q", input.CookieBehavior)
	}

	if input.IndexFrequency == 0 {
		input.IndexFrequency = defaultIndexFrequency
	}
	if input.ShortsRule == "" {
		input.ShortsRule = domain.InclusionInclude
	}
	if input.LivestreamsRule == "" {
		input.LivestreamsRule = domain.InclusionInclude
	}
	if !input.ShortsRule.IsValid() {
		return input, fmt.Errorf("invalid shorts rule %q", input.ShortsRule)
	}
	if !input.LivestreamsRule.IsValid() {
		return input, fmt.Errorf("invalid livestreams rule %q", input.LivestreamsRule)
	}

	if input.TitleFilterPattern != "" {
		if _, err := regexp.Compile(input.TitleFilterPattern); err != nil {
			return input, fmt.Errorf("invalid title filter pattern: %w", err)
		}
	}
	if _, err := s.deps.Profiles.Get(ctx, input.MediaProfileID); err != nil {
		return input, fmt.Errorf("media profile: %w", err)
	}
	return input, nil
}

// CreateProfile validates the profile's naming template and persists it.
func (s *Service) CreateProfile(ctx context.Context, profile domain.MediaProfile) (domain.MediaProfile, error) {
	profile = withMetadataDefault(profile)
	if err := s.validateProfile(profile); err != nil {
		return domain.MediaProfile{}, err
	}
	now := s.deps.Clock.Now()
	profile.CreatedAt = now
	profile.UpdatedAt = now

	id, err := s.deps.Profiles.Create(ctx, profile)
	if err != nil {
		return domain.MediaProfile{}, fmt.Errorf("create profile: %w", err)
	}
	profile.ID = id
	return profile, nil
}

// dashboardListLimit caps how many items each dashboard panel shows.
const dashboardListLimit = 12

// retentionQueueLimit caps how many items the retention queue panel shows.
const retentionQueueLimit = 30

// Overview assembles the dashboard summary in one call: source count, per-status
// counts, storage used, and the in-flight, queued, and recently archived items.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	sources, err := s.deps.Sources.List(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("overview sources: %w", err)
	}
	counts, err := s.deps.Media.CountsByStatus(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("overview counts: %w", err)
	}
	storage, err := s.deps.Media.TotalDownloadedBytes(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("overview storage: %w", err)
	}
	downloading, err := s.deps.Media.ListWithSource(ctx, MediaQuery{Status: domain.MediaDownloading, Limit: dashboardListLimit})
	if err != nil {
		return Overview{}, fmt.Errorf("overview downloading: %w", err)
	}
	queued, err := s.deps.Media.ListWithSource(ctx, MediaQuery{Status: domain.MediaPending, Limit: dashboardListLimit})
	if err != nil {
		return Overview{}, fmt.Errorf("overview queued: %w", err)
	}
	recent, err := s.deps.Media.ListWithSource(ctx, MediaQuery{Status: domain.MediaDownloaded, Limit: dashboardListLimit})
	if err != nil {
		return Overview{}, fmt.Errorf("overview recent: %w", err)
	}
	retentionQueue, err := s.deps.Media.ListSlatedForDeletion(ctx, retentionQueueLimit)
	if err != nil {
		return Overview{}, fmt.Errorf("overview retention queue: %w", err)
	}

	total := 0
	for _, count := range counts {
		total += count
	}
	return Overview{
		SourceCount:    len(sources),
		Counts:         counts,
		TotalMedia:     total,
		StorageBytes:   storage,
		Downloading:    downloading,
		Queued:         queued,
		Recent:         recent,
		RetentionQueue: retentionQueue,
	}, nil
}

// ListMedia returns media newest-first, optionally filtered to a single status.
func (s *Service) ListMedia(ctx context.Context, query MediaQuery) ([]MediaListItem, error) {
	return s.deps.Media.ListWithSource(ctx, query)
}

// SourceStats returns what each source has downloaded, keyed by source id.
func (s *Service) SourceStats(ctx context.Context) (map[int64]SourceStats, error) {
	return s.deps.Media.StatsBySource(ctx)
}

// GetMedia returns a single archived item with its source name.
func (s *Service) GetMedia(ctx context.Context, id int64) (MediaListItem, error) {
	return s.deps.Media.GetWithSource(ctx, id)
}

// RetryMedia puts an item back in the download queue. It exists so a failed or
// unavailable video is never a dead end in the UI: whatever went wrong, the user
// has a way to ask for it again once they have addressed the cause.
func (s *Service) RetryMedia(ctx context.Context, id int64) error {
	media, err := s.deps.Media.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get media: %w", err)
	}

	now := s.deps.Clock.Now()
	if err := s.deps.Media.SetStatus(ctx, id, domain.MediaPending, now); err != nil {
		return fmt.Errorf("reset media status: %w", err)
	}

	task := jobs.NewTask(jobs.TaskDownloadMedia, now).
		ForSource(media.SourceID).ForMedia(id)
	task.Priority = scanNowPriority
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return fmt.Errorf("enqueue retry download: %w", err)
	}
	return nil
}

// RetryAllFailed requeues every failed task for a source within its configured
// timeframe. It looks up the source, computes its effective cutoff, and delegates
// to the queue maintain layer. A nil cutoff (no window and no fixed date) refuses
// the action so the user cannot accidentally requeue an unbounded set.
func (s *Service) RetryAllFailed(ctx context.Context, sourceID int64) (int, error) {
	source, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return 0, fmt.Errorf("get source: %w", err)
	}

	cutoff := source.EffectiveCutoff(s.deps.Clock.Now())
	if cutoff == nil {
		return 0, fmt.Errorf("source %d has no configured timeframe — set CutoffWindow or DownloadCutoff", sourceID)
	}

	n, err := s.deps.Queue.RetryAllFailed(ctx, sourceID, *cutoff, s.deps.Clock.Now())
	if err != nil {
		return 0, fmt.Errorf("retry all failed: %w", err)
	}
	return n, nil
}

// GetProfile returns a single profile by id.
func (s *Service) GetProfile(ctx context.Context, id int64) (domain.MediaProfile, error) {
	return s.deps.Profiles.Get(ctx, id)
}

// ListProfiles returns all media profiles.
func (s *Service) ListProfiles(ctx context.Context) ([]domain.MediaProfile, error) {
	return s.deps.Profiles.List(ctx)
}

// UpdateProfile validates and persists changes to an existing profile.
func (s *Service) UpdateProfile(ctx context.Context, profile domain.MediaProfile) error {
	profile = withMetadataDefault(profile)
	if err := s.validateProfile(profile); err != nil {
		return err
	}
	profile.UpdatedAt = s.deps.Clock.Now()
	if err := s.deps.Profiles.Update(ctx, profile); err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	return nil
}

// DeleteProfile removes a profile by id.
func (s *Service) DeleteProfile(ctx context.Context, id int64) error {
	return s.deps.Profiles.Delete(ctx, id)
}

// validateProfile checks the fields that must be well-formed before a profile is
// saved: a valid kind, SponsorBlock mode, and naming template.
func (s *Service) validateProfile(profile domain.MediaProfile) error {
	if !profile.Kind.IsValid() {
		return fmt.Errorf("invalid media kind %q", profile.Kind)
	}
	if !profile.SponsorBlockMode.IsValid() {
		return fmt.Errorf("invalid sponsorblock mode %q", profile.SponsorBlockMode)
	}
	if !profile.MetadataFormat.IsValid() {
		return fmt.Errorf("invalid metadata format %q", profile.MetadataFormat)
	}
	if err := s.deps.Naming.Validate(profile.OutputPathTemplate); err != nil {
		return fmt.Errorf("output path template: %w", err)
	}
	return nil
}

// withMetadataDefault fills an unset metadata format with the episode layout,
// which matches the season-based output template, keeping older or
// programmatically-built profiles valid.
func withMetadataDefault(profile domain.MediaProfile) domain.MediaProfile {
	if profile.MetadataFormat == "" {
		profile.MetadataFormat = domain.MetadataEpisode
	}
	return profile
}

// IndexSource scans a source's remote collection, records newly discovered,
// rule-passing items as pending media, and enqueues a download task for each. It
// wraps and returns errors so the index task retries on transient failure.
func (s *Service) IndexSource(ctx context.Context, sourceID int64) error {
	source, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if _, err := s.deps.Profiles.Get(ctx, source.MediaProfileID); err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	// Resolve the rolling cutoff window to a concrete date before scanning: it
	// both narrows the scan itself and is what the per-item filter compares
	// against, so the two can never disagree.
	now := s.deps.Clock.Now()
	source.DownloadCutoff = source.EffectiveCutoff(now)

	entries, err := s.deps.Runner.Index(ctx, source.URL, ytdlp.IndexOptions{
		CookiesPath: s.cookieArgFor(source),
		DateAfter:   cutoffDateArg(source.DownloadCutoff),
	})
	if errors.Is(err, ytdlp.ErrThrottled) {
		slog.WarnContext(ctx, "provider throttling detected during scan; backing off",
			"source_id", source.ID, "name", source.Name,
			"until", now.Add(throttleBackoff), "cause", err)
		return jobs.Defer(now.Add(throttleBackoff), throttledReason)
	}
	if err != nil {
		return fmt.Errorf("index %q: %w", source.URL, err)
	}

	matches, err := titleMatcher(source.TitleFilterPattern)
	if err != nil {
		return err
	}

	if err := s.autoNameSource(ctx, &source, entries, now); err != nil {
		return err
	}
	added := 0
	for _, entry := range entries {
		queued, err := s.indexEntry(ctx, source, entry, matches, now)
		if err != nil {
			return err
		}
		if queued {
			added++
		}
	}

	if err := s.deps.Sources.MarkIndexed(ctx, source.ID, now); err != nil {
		return fmt.Errorf("mark indexed: %w", err)
	}
	log.Printf("index source %d (%s): %d entries, %d newly queued", source.ID, source.Name, len(entries), added)
	s.deps.Events.Publish(events.Event{
		Kind:     events.KindSourceIndexed,
		SourceID: source.ID,
		Message:  fmt.Sprintf("indexed %d new item(s)", added),
	})
	return nil
}

// autoNameSource fills a blank source name from the channel name discovered
// during indexing, persisting it. A source whose name the user set is never
// touched, because only a blank name triggers auto-naming.
func (s *Service) autoNameSource(ctx context.Context, source *domain.Source, entries []ytdlp.IndexEntry, now time.Time) error {
	if strings.TrimSpace(source.Name) != "" {
		return nil
	}
	name := deriveSourceName(source.URL, entries)
	if name == "" {
		return nil
	}
	source.Name = name
	source.UpdatedAt = now
	if err := s.deps.Sources.Update(ctx, *source); err != nil {
		return fmt.Errorf("auto-name source: %w", err)
	}
	return nil
}

// deriveSourceName picks a display name for a blank source: the uploader/channel
// name from the first entry that has one, falling back to a readable slug from
// the URL when the collection is empty.
func deriveSourceName(sourceURL string, entries []ytdlp.IndexEntry) string {
	for _, entry := range entries {
		if name := strings.TrimSpace(entry.Uploader); name != "" {
			return name
		}
	}
	return provisionalName(sourceURL)
}

// provisionalName extracts a human-readable name from a channel or playlist URL:
// the @handle, a playlist label, or the last path segment.
func provisionalName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if strings.HasPrefix(segment, "@") {
			return strings.TrimPrefix(segment, "@")
		}
	}
	if parsed.Query().Get("list") != "" {
		return "Playlist"
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := segments[len(segments)-1]
	if last != "" && last != "videos" && last != "playlists" && last != "streams" {
		return last
	}
	return ""
}

// indexEntry records one discovered entry as pending media and enqueues its
// download, unless it is filtered out or already known. It reports whether a
// download was queued.
func (s *Service) indexEntry(ctx context.Context, source domain.Source, entry ytdlp.IndexEntry, matches func(string) bool, now time.Time) (bool, error) {
	meta := metadataFromEntry(entry)
	if !meta.PassesFilters(source, matches) {
		return false, nil
	}

	existing, found, err := s.deps.Media.FindBySource(ctx, source.ID, entry.ExternalID)
	if err != nil {
		return false, fmt.Errorf("check existing media: %w", err)
	}
	if found {
		return s.reconsiderSkipped(ctx, existing, now)
	}

	media := domain.Media{
		SourceID:   source.ID,
		ExternalID: entry.ExternalID,
		Metadata:   meta,
		Status:     domain.MediaPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	id, err := s.deps.Media.Upsert(ctx, media)
	if err != nil {
		return false, fmt.Errorf("upsert media: %w", err)
	}

	task := jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(id)
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return false, fmt.Errorf("enqueue download task: %w", err)
	}
	return true, nil
}

// reconsiderSkipped re-queues an item that was previously passed over but which
// this scan has offered again, reporting whether it queued anything.
//
// An item is only ever skipped because it fell outside the source's date window.
// Reaching this point means the current scan surfaced it and it cleared the
// source's rules, so the window must have widened — and without this, widening it
// would appear to do nothing at all: the scan finds the videos, sees rows already
// exist, and queues none of them. Anything downloaded, failed, or already waiting
// is left alone.
func (s *Service) reconsiderSkipped(ctx context.Context, media domain.Media, now time.Time) (bool, error) {
	if media.Status != domain.MediaSkipped {
		return false, nil
	}

	if err := s.deps.Media.SetStatus(ctx, media.ID, domain.MediaPending, now); err != nil {
		return false, fmt.Errorf("requeue previously skipped media %d: %w", media.ID, err)
	}
	task := jobs.NewTask(jobs.TaskDownloadMedia, now).
		ForSource(media.SourceID).ForMedia(media.ID)
	if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
		return false, fmt.Errorf("enqueue previously skipped media %d: %w", media.ID, err)
	}
	slog.InfoContext(ctx, "re-queued a video the widened window now includes",
		"media_id", media.ID, "title", media.Metadata.Title)
	return true, nil
}

// metadataFromEntry maps a yt-dlp index entry onto domain metadata.
func metadataFromEntry(entry ytdlp.IndexEntry) domain.MediaMetadata {
	return domain.MediaMetadata{
		Title:        entry.Title,
		Description:  entry.Description,
		Uploader:     entry.Uploader,
		UploadDate:   entry.UploadDate,
		Duration:     entry.Duration,
		IsShort:      entry.IsShort,
		IsLivestream: entry.IsLivestream,
	}
}

// DownloadMedia fetches one pending item, records the result, writes its
// sidecar metadata, regenerates the source feed, and notifies the user. On
// download failure it marks the item failed and returns a wrapped error so the
// download task retries.
func (s *Service) DownloadMedia(ctx context.Context, mediaID int64) error {
	media, err := s.deps.Media.Get(ctx, mediaID)
	if err != nil {
		return fmt.Errorf("get media: %w", err)
	}
	source, err := s.deps.Sources.Get(ctx, media.SourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	profile, err := s.deps.Profiles.Get(ctx, source.MediaProfileID)
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	now := s.deps.Clock.Now()
	if err := s.deps.Media.SetStatus(ctx, mediaID, domain.MediaDownloading, now); err != nil {
		return fmt.Errorf("set downloading: %w", err)
	}
	s.publishProgress(media, 0)

	media, err = s.ensureMetadata(ctx, source, media, now)
	if err != nil {
		return s.recordFailure(ctx, mediaID, media, err, now)
	}
	// Now that the real upload date is known, apply the source's window here
	// rather than paying for a download that yt-dlp would only refuse. Indexing a
	// channel is deliberately shallow and yields no dates, so a first scan queues
	// the entire back catalogue; this is where most of it is discarded, and doing
	// it in-process rather than by launching yt-dlp again halves the work.
	if isOutsideWindow(source, media, now) {
		return s.markSkipped(ctx, mediaID, media, now)
	}

	// Only now, once this item is known to be one we actually want, does it wait
	// its turn. Pacing the check itself would make discarding a back catalogue —
	// hundreds of items, each rejected without downloading anything — take days.
	if deferral := s.awaitDownloadSlot(ctx, mediaID, media, now); deferral != nil {
		return deferral
	}

	rel, err := s.renderOutputPath(ctx, profile, source, media)
	if err != nil {
		return fmt.Errorf("render output path: %w", err)
	}
	// The destination is handed to yt-dlp as a media root plus this relative
	// template, never as one absolute path: yt-dlp ignores the scratch-directory
	// setting whenever the output template is absolute, and that scratch
	// directory is what keeps partial downloads off the media volume.
	//
	// The directory is deliberately NOT created here. Most items a large channel
	// offers are never downloaded — they fall outside the source's date window —
	// and creating the folder up front left an empty "Season 2019" through
	// "Season 2025" tree for every year the channel has existed. yt-dlp creates
	// the directory itself when it actually has a file to write.
	relative := filepath.FromSlash(rel)

	opts := s.downloadOptions(profile, source, relative)
	opts.DateAfter = cutoffDateArg(source.EffectiveCutoff(now))
	res, err := s.deps.Runner.Download(ctx, mediaURLFor(source, media), opts, func(percent float64) {
		s.publishProgress(media, percent)
	})
	if errors.Is(err, ytdlp.ErrFilteredOut) {
		return s.markSkipped(ctx, mediaID, media, now)
	}
	if err != nil {
		return s.recordFailure(ctx, mediaID, media, err, now)
	}

	if err := s.deps.Media.MarkDownloaded(ctx, mediaID, res.FilePath, res.FileSize, now); err != nil {
		return fmt.Errorf("mark downloaded: %w", err)
	}
	s.finalizeDownload(ctx, source, media, res.FilePath, profile)
	s.deps.Events.Publish(events.Event{
		Kind:    events.KindMediaCompleted,
		MediaID: mediaID,
		Title:   media.Metadata.Title,
	})
	return nil
}

// awaitDownloadSlot asks the pacer whether this download may start. When it may
// not, the item is returned to pending and a jobs.Deferral is returned for the
// worker to reschedule — the worker is then free for other work rather than
// asleep for what may be a long interval.
//
// Returning the item to pending matters for the UI as much as the data: an item
// left marked "downloading" while it waits its turn would show a dashboard full
// of downloads that are not happening.
func (s *Service) awaitDownloadSlot(ctx context.Context, mediaID int64, media domain.Media, now time.Time) error {
	if s.deps.DownloadPace == nil {
		return nil
	}
	slot, ok := s.deps.DownloadPace.TryClaim()
	if ok {
		return nil
	}
	if err := s.deps.Media.SetStatus(ctx, mediaID, domain.MediaPending, now); err != nil {
		return fmt.Errorf("return media to pending: %w", err)
	}
	s.deps.Events.Publish(events.Event{
		Kind:     events.KindMediaProgress,
		SourceID: media.SourceID,
		MediaID:  mediaID,
		Title:    media.Metadata.Title,
	})
	return jobs.Defer(slot, waitingForSlotReason)
}

// waitingForSlotReason is shown on the job's detail view while it waits, so a
// deliberately delayed download reads as deliberate rather than stalled.
const waitingForSlotReason = "waiting for its turn: downloads are deliberately spaced out"

// ensureMetadata fills in details the collection index could not supply. Indexing
// uses yt-dlp's flat mode for speed, which omits the upload date — and the output
// path template is built from it, so without this every file would be named
// "0001-01-01" and filed under "Season 0001". One extra lookup per item, only
// when the date is actually missing.
func (s *Service) ensureMetadata(ctx context.Context, source domain.Source, media domain.Media, now time.Time) (domain.Media, error) {
	if !media.Metadata.UploadDate.IsZero() {
		return media, nil
	}

	entry, err := s.deps.Runner.Metadata(ctx, mediaURLFor(source, media), s.cookieArgFor(source))
	if err != nil {
		return media, fmt.Errorf("fetch metadata: %w", err)
	}

	media.Metadata = mergeMetadata(media.Metadata, metadataFromEntry(entry))
	media.UpdatedAt = now
	if _, err := s.deps.Media.Upsert(ctx, media); err != nil {
		return media, fmt.Errorf("persist refreshed metadata: %w", err)
	}
	return media, nil
}

// renderOutputPath renders a media item's destination path, relative to the media
// directory. It resolves the item's rank among same-day uploads so a season-based
// template can give each one a distinct episode number.
//
// Every caller that needs a path goes through here — downloading and adopting
// alike — because the two must agree exactly or an already-downloaded file looks
// missing.
func (s *Service) renderOutputPath(ctx context.Context, profile domain.MediaProfile, source domain.Source, media domain.Media) (string, error) {
	context := naming.NewContext(source.Name, media)
	if index, err := s.deps.Media.SameDayIndex(ctx, media.ID); err == nil {
		context = context.WithSameDayIndex(index)
	}
	return s.deps.Naming.Render(profile.OutputPathTemplate, context)
}

// isOutsideWindow reports whether an item was published before the source's
// rolling cutoff. An unknown upload date is never treated as too old, so a
// provider that withholds the date can never cause a silent mass skip.
func isOutsideWindow(source domain.Source, media domain.Media, now time.Time) bool {
	cutoff := source.EffectiveCutoff(now)
	if cutoff == nil || media.Metadata.UploadDate.IsZero() {
		return false
	}
	return media.Metadata.UploadDate.Before(*cutoff)
}

// mergeMetadata layers freshly fetched details over what indexing already knew,
// keeping existing values wherever the fetch came back blank so a partial
// response can never erase good data.
func mergeMetadata(existing, fetched domain.MediaMetadata) domain.MediaMetadata {
	merged := existing
	if !fetched.UploadDate.IsZero() {
		merged.UploadDate = fetched.UploadDate
	}
	if fetched.Title != "" {
		merged.Title = fetched.Title
	}
	if fetched.Description != "" {
		merged.Description = fetched.Description
	}
	if fetched.Uploader != "" {
		merged.Uploader = fetched.Uploader
	}
	if fetched.Duration > 0 {
		merged.Duration = fetched.Duration
	}
	return merged
}

// downloadOptions assembles yt-dlp download options from a profile and source.
// relativeOut is the destination path beneath the media directory, without an
// extension.
func (s *Service) downloadOptions(profile domain.MediaProfile, source domain.Source, relativeOut string) ytdlp.DownloadOptions {
	return ytdlp.DownloadOptions{
		Format:           profile.QualityFormat,
		OutputPath:       relativeOut,
		HomeDir:          s.deps.MediaDir,
		TempDir:          s.deps.TempDir,
		AudioOnly:        profile.Kind == domain.MediaAudio,
		EmbedMetadata:    profile.EmbedMetadata,
		EmbedThumbnail:   profile.EmbedThumbnail,
		WriteThumbnail:   profile.WriteThumbnail,
		EmbedSubtitles:   profile.EmbedSubtitles,
		SubtitleLangs:    profile.SubtitleLanguages,
		SponsorBlockArgs: s.deps.SponsorBlock.Args(profile.SponsorBlockMode, profile.SponsorBlockCategories),
		ExtraArgs:        profile.ExtraYtdlpArgs,
		CookiesPath:      s.cookieArgFor(source),
	}
}

// cutoffDateArg formats a cutoff time as yt-dlp's YYYYMMDD --dateafter value, or
// empty when there is no cutoff.
func cutoffDateArg(cutoff *time.Time) string {
	if cutoff == nil {
		return ""
	}
	return cutoff.Format("20060102")
}

// markSkipped records that yt-dlp declined to download an item (it fell outside
// the source's date cutoff). It is a settled, successful outcome, so no error is
// returned and the task is not retried.
func (s *Service) markSkipped(ctx context.Context, mediaID int64, media domain.Media, now time.Time) error {
	if err := s.deps.Media.SetStatus(ctx, mediaID, domain.MediaSkipped, now); err != nil {
		return fmt.Errorf("mark skipped: %w", err)
	}
	log.Printf("download media %d (%s): skipped (outside date cutoff)", mediaID, media.Metadata.Title)
	return nil
}

// recordFailure settles a failed download attempt. It is the single place that
// decides retryable from permanent, so every step of the download — the metadata
// lookup as much as the transfer itself — treats a members-only or removed video
// the same way. Routing around it is how such a video ends up burning three
// attempts and reporting a generic failure.
func (s *Service) recordFailure(ctx context.Context, mediaID int64, media domain.Media, cause error, now time.Time) error {
	if errors.Is(cause, ytdlp.ErrUnavailable) {
		return s.markUnavailable(ctx, mediaID, media, cause, now)
	}
	if errors.Is(cause, ytdlp.ErrThrottled) {
		return s.deferThrottled(ctx, mediaID, media, cause, now)
	}
	return s.recordDownloadFailure(ctx, mediaID, media, cause, now)
}

// throttleBackoff is how long to stand down after the provider rate-limits or
// bot-checks us. Long enough to look like a person walking away, short enough
// that the queue recovers the same day.
const throttleBackoff = 45 * time.Minute

// throttledReason explains the pause on the job's detail view, so a backed-off
// task reads as deliberate caution rather than a stall.
const throttledReason = "provider is rate-limiting us; backing off before trying again"

// deferThrottled stands the task down instead of failing it. A 429 or bot-check
// is the provider telling us to slow down, and the worst response is spending
// the retry budget proving it right — the deferral consumes no attempt, and the
// item returns to pending so the dashboard does not show a download that is not
// happening.
func (s *Service) deferThrottled(ctx context.Context, mediaID int64, media domain.Media, cause error, now time.Time) error {
	if err := s.deps.Media.SetStatus(ctx, mediaID, domain.MediaPending, now); err != nil {
		return fmt.Errorf("return throttled media to pending: %w", err)
	}
	slog.WarnContext(ctx, "provider throttling detected; backing off",
		"media_id", mediaID, "title", media.Metadata.Title,
		"until", now.Add(throttleBackoff), "cause", cause)
	return jobs.Defer(now.Add(throttleBackoff), throttledReason)
}

// markUnavailable records that the provider refuses to serve this item at all.
// The outcome is settled — no error is returned, so the task does not spend its
// retry budget re-asking a question already answered — but the reason is stored
// and shown, because "unavailable" is something the user may want to act on
// (sign in, add cookies, join the channel).
func (s *Service) markUnavailable(ctx context.Context, mediaID int64, media domain.Media, cause error, now time.Time) error {
	if err := s.deps.Media.SetError(ctx, mediaID, domain.MediaUnavailable, cause.Error(), now); err != nil {
		return fmt.Errorf("mark unavailable: %w", err)
	}
	s.deps.Events.Publish(events.Event{
		Kind:    events.KindMediaFailed,
		MediaID: mediaID,
		Title:   media.Metadata.Title,
		Message: cause.Error(),
	})
	slog.WarnContext(ctx, "media unavailable from provider",
		"media_id", mediaID, "title", media.Metadata.Title, "reason", cause.Error())
	return nil
}

// recordDownloadFailure marks the item failed, publishes a failure event, and
// returns a wrapped error so the task will be retried.
func (s *Service) recordDownloadFailure(ctx context.Context, mediaID int64, media domain.Media, cause error, now time.Time) error {
	if err := s.deps.Media.SetError(ctx, mediaID, domain.MediaFailed, cause.Error(), now); err != nil {
		return fmt.Errorf("set error after download failure (%v): %w", cause, err)
	}
	s.deps.Events.Publish(events.Event{
		Kind:    events.KindMediaFailed,
		MediaID: mediaID,
		Title:   media.Metadata.Title,
		Message: cause.Error(),
	})
	return fmt.Errorf("download media %d: %w", mediaID, cause)
}

// finalizeDownload performs the best-effort side effects that follow a
// successful download: sidecar metadata, feed regeneration, and notification.
// Failures here are logged but do not fail the download, which already succeeded.
func (s *Service) finalizeDownload(ctx context.Context, source domain.Source, media domain.Media, filePath string, profile domain.MediaProfile) {
	if _, err := s.deps.Metadata.WriteFor(ctx, filePath, media, source.Name, profile.MetadataFormat); err != nil {
		log.Printf("library: write metadata for %q: %v", filePath, err)
	}
	s.writeShowSidecars(ctx, source, filePath, profile)

	items, err := s.deps.Media.ListBySource(ctx, source.ID)
	if err != nil {
		log.Printf("library: list media for feed regen (source %d): %v", source.ID, err)
	} else if err := s.deps.Feed.WriteFeed(ctx, source, items); err != nil {
		log.Printf("library: write feed for source %d: %v", source.ID, err)
	}

	if err := s.deps.Hook.Run(ctx, profile.PostDownloadCommand, filePath); err != nil {
		log.Printf("library: post-download hook for %q: %v", filePath, err)
	}

	if err := s.deps.Notifier.Notify(ctx, "Downloaded", media.Metadata.Title); err != nil {
		log.Printf("library: notify for %q: %v", media.Metadata.Title, err)
	}
}

// writeShowSidecars keeps everything that describes the series as a whole up to
// date at the channel folder's root: the metadata file and the artwork.
//
// Left to the folder name alone, a server matches it against an online database
// and takes whatever comes back: "Channel 5 with Andrew Callaghan" came back as
// the anime "A-Channel". Only a series library has any use for this, and only a
// layout with a channel folder has anywhere to put it.
func (s *Service) writeShowSidecars(ctx context.Context, source domain.Source, filePath string, profile domain.MediaProfile) {
	if profile.MetadataFormat == domain.MetadataMovie {
		return
	}
	showDir, ok := s.showDirFor(filePath)
	if !ok {
		return
	}
	if _, err := s.deps.Metadata.WriteShow(ctx, showDir, source.Name, source.URL); err != nil {
		log.Printf("library: write show metadata in %q: %v", showDir, err)
	}
	s.writeShowArtwork(ctx, source, showDir)
}

// writeShowArtwork gives the show folder its poster, backdrop, and season
// posters, fetching them from the provider only when they are not already there.
//
// Naming the series locally stops a media server from inventing one, but it also
// stops it from fetching pictures: an agent reading local metadata does no online
// lookup, so nothing supplies channel art unless sub_scribe puts it on disk. The
// per-video thumbnails yt-dlp already writes cover the episodes; this covers the
// show and its seasons, which would otherwise stay blank placeholders.
func (s *Service) writeShowArtwork(ctx context.Context, source domain.Source, showDir string) {
	if !s.deps.Artwork.NeedsArt(showDir) {
		return
	}
	art, err := s.deps.Runner.Artwork(ctx, source.URL, s.cookieArgFor(source))
	if err != nil {
		log.Printf("library: fetch artwork for source %d: %v", source.ID, err)
		return
	}
	if _, err := s.deps.Artwork.WriteArt(ctx, showDir, art); err != nil {
		log.Printf("library: write artwork in %q: %v", showDir, err)
	}
}

// showDirFor returns the folder a media server treats as the series root: the
// first directory beneath the media root. It reports false for a flat layout,
// where the file sits directly in the media root and there is no series folder
// to describe.
func (s *Service) showDirFor(filePath string) (string, bool) {
	rel, err := filepath.Rel(s.deps.MediaDir, filePath)
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 || parts[0] == "" || parts[0] == ".." {
		return "", false
	}
	return filepath.Join(s.deps.MediaDir, parts[0]), true
}

// publishProgress emits a media-progress event with the given percentage.
//
// Both ids travel with it: the item being downloaded is identified by its media
// id, while the source id lets a listener that groups by channel react too.
func (s *Service) publishProgress(media domain.Media, percent float64) {
	s.deps.Events.Publish(events.Event{
		Kind:     events.KindMediaProgress,
		MediaID:  media.ID,
		SourceID: media.SourceID,
		Percent:  percent,
	})
}

// EnforceRetention deletes downloaded media older than the source's retention
// window and marks the items skipped. It is a no-op when retention is disabled.
// EnforceRedownload re-queues downloaded media older than the source profile's
// redownload age. It deletes the stale file and moves the item back to pending so
// the normal download flow fetches a fresh copy. A zero redownload age is a no-op.
func (s *Service) EnforceRedownload(ctx context.Context, sourceID int64) error {
	source, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	profile, err := s.deps.Profiles.Get(ctx, source.MediaProfileID)
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}
	if profile.RedownloadAfter == 0 {
		return nil
	}

	items, err := s.deps.Media.ListBySource(ctx, source.ID)
	if err != nil {
		return fmt.Errorf("list media: %w", err)
	}

	now := s.deps.Clock.Now()
	cutoff := now.Add(-profile.RedownloadAfter)
	for _, item := range items {
		if !isDueForRedownload(item, cutoff) {
			continue
		}
		if err := removeFile(item.FilePath); err != nil {
			log.Printf("library: remove for redownload %q: %v", item.FilePath, err)
		}
		if err := s.deps.Media.SetStatus(ctx, item.ID, domain.MediaPending, now); err != nil {
			return fmt.Errorf("requeue for redownload: %w", err)
		}
		task := jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(item.ID)
		if _, err := s.deps.Tasks.Enqueue(ctx, task); err != nil {
			return fmt.Errorf("enqueue redownload: %w", err)
		}
	}
	return nil
}

// isDueForRedownload reports whether a downloaded item is older than the cutoff
// and therefore eligible to be fetched again.
func isDueForRedownload(item domain.Media, cutoff time.Time) bool {
	return item.Status == domain.MediaDownloaded &&
		item.DownloadedAt != nil && item.DownloadedAt.Before(cutoff)
}

func (s *Service) EnforceRetention(ctx context.Context, sourceID int64) error {
	source, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if source.RetentionAfter == 0 {
		return nil
	}

	items, err := s.deps.Media.ListBySource(ctx, source.ID)
	if err != nil {
		return fmt.Errorf("list media: %w", err)
	}

	now := s.deps.Clock.Now()
	cutoff := now.Add(-source.RetentionAfter)
	for _, item := range items {
		if !isExpired(item, cutoff) {
			continue
		}
		if err := removeFile(item.FilePath); err != nil {
			log.Printf("library: remove expired media %q: %v", item.FilePath, err)
			continue
		}
		if err := s.deps.Media.SetStatus(ctx, item.ID, domain.MediaSkipped, now); err != nil {
			return fmt.Errorf("mark skipped: %w", err)
		}
	}
	return nil
}

// isExpired reports whether a downloaded item is older than the retention cutoff.
func isExpired(item domain.Media, cutoff time.Time) bool {
	if item.Status != domain.MediaDownloaded || item.DownloadedAt == nil {
		return false
	}
	return item.DownloadedAt.Before(cutoff)
}

// removeFile deletes a file, treating an already-absent file as success.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// cookieArgFor returns the cookies file to pass to yt-dlp for a source, or empty
// when cookies are disabled, unconfigured, or the file is missing on disk.
func (s *Service) cookieArgFor(source domain.Source) string {
	switch source.CookieBehavior {
	case domain.CookieAllOperations, domain.CookieWhenNeeded:
		if s.deps.CookiesPath == "" {
			return ""
		}
		if _, err := os.Stat(s.deps.CookiesPath); err != nil {
			return ""
		}
		return s.deps.CookiesPath
	default:
		return ""
	}
}

// mediaURLFor returns the watch URL for a media item, delegating to the domain
// so every consumer builds the same link.
func mediaURLFor(_ domain.Source, media domain.Media) string {
	return media.WatchURL()
}

// titleMatcher builds a predicate from a source's title-filter pattern. An empty
// pattern allows everything; a non-empty one is compiled once and reused.
func titleMatcher(pattern string) (func(string) bool, error) {
	if pattern == "" {
		return func(string) bool { return true }, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile title filter: %w", err)
	}
	return re.MatchString, nil
}
