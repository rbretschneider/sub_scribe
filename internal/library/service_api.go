package library

import (
	"context"
	"time"

	"sub_scribe/internal/domain"
)

// The service exposes several small, client-focused interfaces rather than one
// broad one (Interface Segregation): the web layer depends on the CRUD-shaped
// interfaces, while the background task handlers depend only on the action
// interfaces. A single concrete Service implements them all.

// SourceService handles user-driven source management. AddSource must return
// immediately after persisting and enqueuing an index task — it never blocks on
// yt-dlp, which is the core fix for the original app's add-time lag.
type SourceService interface {
	AddSource(ctx context.Context, input AddSourceInput) (domain.Source, error)
	GetSource(ctx context.Context, id int64) (domain.Source, error)
	ListSources(ctx context.Context) ([]domain.Source, error)
	UpdateSource(ctx context.Context, id int64, input AddSourceInput) (domain.Source, error)
	DeleteSource(ctx context.Context, id int64, opts DeleteSourceOptions) error
	// SetSourceEnabled pauses (false) or resumes (true) scanning of a source
	// without touching its settings or its downloaded media.
	SetSourceEnabled(ctx context.Context, id int64, enabled bool) error
	// RequestScan enqueues an immediate index of the source, out of schedule.
	RequestScan(ctx context.Context, id int64) error
	// RequestRename enqueues a pass that moves the source's existing files to the
	// paths its naming template currently describes.
	RequestRename(ctx context.Context, id int64) error
	// DownloadVideo queues one pasted video for immediate download, returning
	// its media id. It rejects anything that is not a single-video URL with
	// ErrNotAVideoURL.
	DownloadVideo(ctx context.Context, rawURL string) (int64, error)
}

// DeleteSourceOptions controls how much of a source is removed. The zero value
// removes only sub_scribe's own records, which is the reversible choice: the
// videos stay on disk and a re-added source adopts them again.
type DeleteSourceOptions struct {
	// DeleteFiles also removes the downloaded media files from disk. This is not
	// reversible, so it is never the default and callers must opt in explicitly.
	DeleteFiles bool
}

// SourceStats summarises what a source has on disk, so the UI can say exactly
// what deleting it would destroy.
type SourceStats struct {
	Files int
	Bytes int64
}

// ProfileService handles media-profile management.
type ProfileService interface {
	CreateProfile(ctx context.Context, profile domain.MediaProfile) (domain.MediaProfile, error)
	GetProfile(ctx context.Context, id int64) (domain.MediaProfile, error)
	ListProfiles(ctx context.Context) ([]domain.MediaProfile, error)
	UpdateProfile(ctx context.Context, profile domain.MediaProfile) error
	DeleteProfile(ctx context.Context, id int64) error
}

// Indexer scans a source and records newly discovered, rule-passing media as
// pending, enqueuing a download task for each. Invoked by the index task handler.
type Indexer interface {
	IndexSource(ctx context.Context, sourceID int64) error
}

// Downloader fetches one pending media item and records the result. Invoked by
// the download task handler.
type Downloader interface {
	DownloadMedia(ctx context.Context, mediaID int64) error
}

// Retainer enforces a source's retention policy by deleting downloaded media
// older than its configured age. Invoked by the cleanup task handler.
type Retainer interface {
	EnforceRetention(ctx context.Context, sourceID int64) error
}

// Redownloader re-queues a source's downloaded media once it is older than the
// profile's redownload age, so content is re-fetched to pick up quality
// improvements. Invoked by the redownload task handler.
type Redownloader interface {
	EnforceRedownload(ctx context.Context, sourceID int64) error
}

// JobPruner drops finished queue entries past their retention window, returning
// how many were removed. Invoked by the prune task handler.
type JobPruner interface {
	PruneJobs(ctx context.Context) (int, error)
}

// Renamer brings a source's existing files into line with its current naming
// template. Invoked by the rename task handler.
type Renamer interface {
	ApplyNamingTemplate(ctx context.Context, sourceID int64) (RenameReport, error)
}

// MediaListItem pairs a media item with its source's display name, for the
// library and dashboard read views where the channel name is shown alongside the
// video.
type MediaListItem struct {
	Media      domain.Media
	SourceName string
	// Expiration is the computed retention cutoff time. Only set for items
	// returned by ListSlatedForDeletion.
	Expiration time.Time
}

// Overview is the dashboard summary: headline counts plus the in-flight and
// most-recently-archived items, assembled in one call so the handler makes a
// single request.
type Overview struct {
	SourceCount    int
	Counts         map[domain.MediaStatus]int
	TotalMedia     int
	StorageBytes   int64
	Downloading    []MediaListItem
	Queued         []MediaListItem
	Recent         []MediaListItem
	RetentionQueue []MediaListItem
}

// MediaQuery narrows a media listing. The zero value returns everything,
// newest first.
type MediaQuery struct {
	// Status limits the listing to one media status; empty means all.
	Status domain.MediaStatus
	// SourceID limits the listing to one source's items; zero means all sources.
	SourceID int64
	// Search keeps only items whose title contains this text,
	// case-insensitively; empty keeps everything.
	Search string
	// Limit caps the number of rows; zero or less means no cap.
	Limit int
}

// LibraryReader serves the read-only views of the archive. The web layer depends
// on it for the dashboard and library screens.
type LibraryReader interface {
	// Overview returns the dashboard summary.
	Overview(ctx context.Context) (Overview, error)
	// ListMedia returns media (newest first) narrowed by whatever parts of the
	// query are set.
	ListMedia(ctx context.Context, query MediaQuery) ([]MediaListItem, error)
	// GetMedia returns one item with its source name, for the detail screen.
	GetMedia(ctx context.Context, id int64) (MediaListItem, error)
	// SourceStats returns what each source has downloaded, keyed by source id, so
	// a delete confirmation can name what is at stake.
	SourceStats(ctx context.Context) (map[int64]SourceStats, error)
	// RetryAllFailed requeues every failed task for a source within its
	// configured timeframe, returning how many were requeued.
	RetryAllFailed(ctx context.Context, sourceID int64) (int, error)
}

// DownloadPacer decides whether a download may start now.
//
// Archiving runs with the user's own account credentials, so the rate downloads
// are started at is a safety setting, not a performance one. TryClaim reports
// the start time and true when a download may begin, or when the next slot
// opens and false when it may not — and taking nothing in the second case, so
// asking does not push the queue further out.
type DownloadPacer interface {
	TryClaim() (time.Time, bool)
}

// MediaService covers the actions a user can take on a single archived item.
// Kept apart from the read interface so the detail page's read path and its
// buttons have separate, minimal contracts.
type MediaService interface {
	// RetryMedia puts a failed or unavailable item back in the download queue.
	RetryMedia(ctx context.Context, id int64) error
}

// AddSourceInput is the validated data required to create or update a source. It
// mirrors the add-source form; the service converts it into a domain.Source.
type AddSourceInput struct {
	Name               string
	URL                string
	CollectionType     domain.CollectionType
	MediaProfileID     int64
	CookieBehavior     domain.CookieBehavior
	IndexFrequency     time.Duration
	DownloadCutoff     *time.Time
	CutoffWindow       time.Duration
	TitleFilterPattern string
	ShortsRule         domain.InclusionRule
	LivestreamsRule    domain.InclusionRule
	RetentionAfter     time.Duration
}
