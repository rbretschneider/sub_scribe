// Package library is sub_scribe's application core: it turns user intent ("track
// this channel") and background triggers ("index is due") into coordinated work
// across the repositories, yt-dlp, naming, and metadata. It depends only on the
// interfaces declared here, so every collaborator is injectable and fakeable.
package library

import (
	"context"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// SourceRepo persists sources. Implemented by the store package.
type SourceRepo interface {
	Create(ctx context.Context, source domain.Source) (int64, error)
	Get(ctx context.Context, id int64) (domain.Source, error)
	List(ctx context.Context) ([]domain.Source, error)
	Update(ctx context.Context, source domain.Source) error
	Delete(ctx context.Context, id int64) error
	DueForIndex(ctx context.Context, now time.Time) ([]domain.Source, error)
	MarkIndexed(ctx context.Context, id int64, now time.Time) error
}

// MediaRepo persists media items. Upsert deduplicates on (source_id,
// external_id) so re-indexing a source never creates duplicates.
type MediaRepo interface {
	Upsert(ctx context.Context, media domain.Media) (int64, error)
	// UpsertBatch records many items in one transaction, returning their ids in
	// input order — the difference between a scan of a large channel spending
	// its time on rows or on one commit.
	UpsertBatch(ctx context.Context, media []domain.Media) ([]int64, error)
	Get(ctx context.Context, id int64) (domain.Media, error)
	// FindBySource returns an existing item for the source, so indexing can decide
	// whether to record a new one or reconsider one already known.
	FindBySource(ctx context.Context, sourceID int64, externalID string) (domain.Media, bool, error)
	ExistsBySource(ctx context.Context, sourceID int64, externalID string) (bool, error)
	ListBySource(ctx context.Context, sourceID int64) ([]domain.Media, error)
	ListByStatus(ctx context.Context, status domain.MediaStatus, limit int) ([]domain.Media, error)
	SetStatus(ctx context.Context, id int64, status domain.MediaStatus, now time.Time) error
	MarkDownloaded(ctx context.Context, id int64, filePath string, size int64, now time.Time) error
	// SetFilePath records that an item's file has moved, without disturbing its
	// status or download history. Upsert deliberately does not update the path —
	// it exists for re-indexing, which must never clobber download state — so
	// moving a file needs its own narrow operation.
	SetFilePath(ctx context.Context, id int64, filePath string, now time.Time) error
	SetError(ctx context.Context, id int64, status domain.MediaStatus, cause string, now time.Time) error

	// CountsByStatus returns the number of media items in each status.
	CountsByStatus(ctx context.Context) (map[domain.MediaStatus]int, error)
	// TotalDownloadedBytes returns the summed file size of downloaded media.
	TotalDownloadedBytes(ctx context.Context) (int64, error)
	// ListWithSource returns media joined to their source name, newest first,
	// narrowed by whatever parts of the query are set.
	ListWithSource(ctx context.Context, query MediaQuery) ([]MediaListItem, error)
	// ListSlatedForDeletion returns media whose source has a retention policy
	// and whose file still exists, ordered by soonest-to-expire first.
	ListSlatedForDeletion(ctx context.Context, limit int) ([]MediaListItem, error)
	// SameDayIndex returns the 1-based rank of an item among those sharing its
	// upload date, so episode numbering can separate same-day uploads.
	SameDayIndex(ctx context.Context, id int64) (int, error)
	// GetWithSource returns one media item joined to its source name.
	GetWithSource(ctx context.Context, id int64) (MediaListItem, error)
	// StatsBySource returns downloaded file counts and sizes keyed by source id.
	StatsBySource(ctx context.Context) (map[int64]SourceStats, error)
}

// ProfileRepo persists media profiles (the how/where of downloading).
type ProfileRepo interface {
	Create(ctx context.Context, profile domain.MediaProfile) (int64, error)
	Get(ctx context.Context, id int64) (domain.MediaProfile, error)
	List(ctx context.Context) ([]domain.MediaProfile, error)
	Update(ctx context.Context, profile domain.MediaProfile) error
	Delete(ctx context.Context, id int64) error
}

// TaskEnqueuer schedules background work. Implemented by the store's task repo;
// declared here (not imported from store) so the core never depends on the
// database. jobs.Task is a plain value type, safe to depend on.
type TaskEnqueuer interface {
	Enqueue(ctx context.Context, task jobs.Task) (int64, error)
	// EnqueueBatch inserts many tasks in one transaction, for the scan path
	// that queues a download per newly discovered video.
	EnqueueBatch(ctx context.Context, tasks []jobs.Task) error
}

// MetadataWriter writes a sidecar metadata file (e.g. Kodi/Jellyfin .nfo) next to
// a downloaded media file, in the layout selected by format. Implemented by the
// metadata package.
// Both methods report whether the file's content actually changed. Rewriting a
// sidecar that is already correct restamps it, which a watching media server
// reads as "re-examine this" — so a pass over the archive needs to know the
// difference between having written and having changed something.
type MetadataWriter interface {
	WriteFor(ctx context.Context, mediaFilePath string, media domain.Media, sourceName string, format domain.MetadataFormat) (changed bool, err error)
	// WriteShow writes the series-level sidecar at a channel folder's root, which
	// is what lets a media server identify the series from local data instead of
	// matching the folder name against an online database.
	WriteShow(ctx context.Context, showDir, name, url string) (changed bool, err error)
}

// ArtworkWriter saves a source's channel imagery into its show folder, under the
// filenames a media server reads as the series poster, backdrop, and season
// posters. Implemented by the artwork package.
//
// NeedsArt is part of the contract rather than an internal detail because it is
// what keeps artwork off the network: the URLs cost a provider call to discover,
// and there is no point making that call for a show whose images are already on
// disk. The caller asks first, and only fetches URLs when the answer is yes.
type ArtworkWriter interface {
	NeedsArt(showDir string) bool
	WriteArt(ctx context.Context, showDir string, art domain.ChannelArtwork) (changed bool, err error)
}

// FeedWriter (re)builds a source's RSS/podcast feed from its downloaded items.
// Implemented by the feed package.
type FeedWriter interface {
	WriteFeed(ctx context.Context, source domain.Source, items []domain.Media) error
}

// Notifier delivers a user notification (e.g. via Apprise). Implemented by the
// notify package. A no-op implementation is used when notifications are disabled.
type Notifier interface {
	Notify(ctx context.Context, title, message string) error
}

// SponsorBlockBuilder converts a profile's SponsorBlock settings into yt-dlp
// arguments. Implemented by the sponsorblock package. It is injected (rather than
// called as a package function) so the core depends only on interfaces declared
// here and every collaborator is uniformly fakeable.
type SponsorBlockBuilder interface {
	Args(mode domain.SponsorBlockMode, categories []domain.SponsorBlockCategory) []string
}

// PostDownloadHook runs a user-configured command after a successful download,
// passing the downloaded file's path. Implemented by the hooks package; the core
// only invokes it when a profile configures a command.
type PostDownloadHook interface {
	Run(ctx context.Context, command, mediaPath string) error
}
