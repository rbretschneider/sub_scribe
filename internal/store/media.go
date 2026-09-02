package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// MediaRepo satisfies library.MediaRepo.
var _ library.MediaRepo = (*MediaRepo)(nil)

// MediaRepo persists media items. It deduplicates on (source_id, external_id) so
// re-indexing a source refreshes metadata without creating duplicate rows or
// clobbering download progress.
type MediaRepo struct {
	sql *sql.DB
}

// mediaColumns is the shared SELECT/RETURNING column list, kept in one place so
// the scan order and the queries can never drift apart.
const mediaColumns = `id, source_id, external_id, title, description, uploader,
	upload_date, duration_seconds, is_short, is_livestream, status, file_path,
	file_size, attempts, last_error, downloaded_at, created_at, updated_at`

// Upsert inserts a media item or, when one already exists for the same source
// and external id, refreshes only its metadata columns. It never overwrites the
// download lifecycle (status, file_path, file_size, downloaded_at) and returns
// the row's stable id.
func (r *MediaRepo) Upsert(ctx context.Context, media domain.Media) (int64, error) {
	var id int64
	err := r.sql.QueryRowContext(ctx,
		`INSERT INTO media(source_id, external_id, title, description, uploader,
			upload_date, duration_seconds, is_short, is_livestream, status, file_path,
			file_size, attempts, last_error, downloaded_at, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source_id, external_id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			uploader = excluded.uploader,
			upload_date = excluded.upload_date,
			duration_seconds = excluded.duration_seconds,
			is_short = excluded.is_short,
			is_livestream = excluded.is_livestream,
			updated_at = excluded.updated_at
		 RETURNING id`,
		media.SourceID, media.ExternalID, media.Metadata.Title,
		media.Metadata.Description, media.Metadata.Uploader,
		toNullUnix(uploadDatePtr(media.Metadata.UploadDate)),
		toDurationSeconds(media.Metadata.Duration),
		boolToInt(media.Metadata.IsShort), boolToInt(media.Metadata.IsLivestream),
		media.Status, media.FilePath, media.FileSize, media.Attempts,
		media.LastError, toNullUnix(media.DownloadedAt),
		media.CreatedAt.Unix(), media.UpdatedAt.Unix(),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert media: %w", err)
	}
	return id, nil
}

// SetFilePath records a media file's new location after it has been moved on
// disk, leaving status, size, and download history untouched.
func (r *MediaRepo) SetFilePath(ctx context.Context, id int64, filePath string, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE media SET file_path = ?, updated_at = ? WHERE id = ?`,
		filePath, now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: set file path for media %d: %w", id, err)
	}
	return nil
}

// Get returns the media item with the given id.
func (r *MediaRepo) Get(ctx context.Context, id int64) (domain.Media, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE id = ?`, id)
	media, err := scanMedia(row)
	if err != nil {
		return domain.Media{}, fmt.Errorf("store: get media %d: %w", id, err)
	}
	return media, nil
}

// FindBySource returns the source's item with externalID, reporting whether one
// exists. Indexing needs the row itself, not just its existence, so it can tell
// a genuinely new video from one already recorded and possibly reconsiderable.
func (r *MediaRepo) FindBySource(ctx context.Context, sourceID int64, externalID string) (domain.Media, bool, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE source_id = ? AND external_id = ?`,
		sourceID, externalID)

	media, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Media{}, false, nil
	}
	if err != nil {
		return domain.Media{}, false, fmt.Errorf("store: find media %s: %w", externalID, err)
	}
	return media, true, nil
}

// ExistsBySource reports whether the source already has an item with externalID.
func (r *MediaRepo) ExistsBySource(ctx context.Context, sourceID int64, externalID string) (bool, error) {
	var exists int
	err := r.sql.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM media WHERE source_id = ? AND external_id = ?)`,
		sourceID, externalID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: exists media: %w", err)
	}
	return exists != 0, nil
}

// ListBySource returns every media item for a source ordered by id.
func (r *MediaRepo) ListBySource(ctx context.Context, sourceID int64) ([]domain.Media, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE source_id = ? ORDER BY id ASC`,
		sourceID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list media by source: %w", err)
	}
	defer rows.Close()
	return scanMediaList(rows)
}

// ListByStatus returns media in the given status ordered by id, capping the
// result at limit rows. A limit of zero or less means no cap.
func (r *MediaRepo) ListByStatus(ctx context.Context, status domain.MediaStatus, limit int) ([]domain.Media, error) {
	query := `SELECT ` + mediaColumns + ` FROM media WHERE status = ? ORDER BY id ASC`
	args := []any{status}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list media by status: %w", err)
	}
	defer rows.Close()
	return scanMediaList(rows)
}

// SetStatus transitions a media item to a new lifecycle status.
func (r *MediaRepo) SetStatus(ctx context.Context, id int64, status domain.MediaStatus, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE media SET status = ?, updated_at = ? WHERE id = ?`,
		status, now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: set media status %d: %w", id, err)
	}
	return nil
}

// MarkDownloaded records a successful download: it stores the file location and
// size, stamps the download time, and moves the item to the downloaded status.
func (r *MediaRepo) MarkDownloaded(ctx context.Context, id int64, filePath string, size int64, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE media SET status = ?, file_path = ?, file_size = ?,
			downloaded_at = ?, last_error = '', updated_at = ? WHERE id = ?`,
		domain.MediaDownloaded, filePath, size, now.Unix(), now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: mark downloaded %d: %w", id, err)
	}
	return nil
}

// SetError records a failed attempt: it stores the cause, counts the attempt,
// and moves the item to the given (typically failed) status.
func (r *MediaRepo) SetError(ctx context.Context, id int64, status domain.MediaStatus, cause string, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE media SET status = ?, last_error = ?, attempts = attempts + 1,
			updated_at = ? WHERE id = ?`,
		status, cause, now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: set media error %d: %w", id, err)
	}
	return nil
}

// CountsByStatus returns the number of media items grouped by status.
func (r *MediaRepo) CountsByStatus(ctx context.Context) (map[domain.MediaStatus]int, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT status, COUNT(*) FROM media GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("store: counts by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[domain.MediaStatus]int)
	for rows.Next() {
		var status domain.MediaStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("store: scan status count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// TotalDownloadedBytes returns the summed file size of downloaded media.
func (r *MediaRepo) TotalDownloadedBytes(ctx context.Context) (int64, error) {
	var total int64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(file_size), 0) FROM media WHERE status = ?`,
		domain.MediaDownloaded,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: total downloaded bytes: %w", err)
	}
	return total, nil
}

// ListWithSource returns media joined to their source name, newest first (by
// download time, falling back to discovery time). An empty status returns every
// item; limit <= 0 removes the cap.
func (r *MediaRepo) ListWithSource(ctx context.Context, q library.MediaQuery) ([]library.MediaListItem, error) {
	query := `SELECT ` + prefixedMediaColumns + `, s.name
		FROM media m JOIN sources s ON s.id = m.source_id WHERE 1=1`
	var args []any
	if q.Status != "" {
		query += ` AND m.status = ?`
		args = append(args, q.Status)
	}
	if q.SourceID > 0 {
		query += ` AND m.source_id = ?`
		args = append(args, q.SourceID)
	}
	if q.Search != "" {
		query += ` AND m.title LIKE ? ESCAPE '\' COLLATE NOCASE`
		args = append(args, "%"+escapeLike(q.Search)+"%")
	}
	query += ` ORDER BY COALESCE(m.downloaded_at, m.created_at) DESC, m.id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list media with source: %w", err)
	}
	defer rows.Close()

	var items []library.MediaListItem
	for rows.Next() {
		media, sourceName, err := scanMediaWithSource(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan media with source: %w", err)
		}
		items = append(items, library.MediaListItem{Media: media, SourceName: sourceName})
	}
	return items, rows.Err()
}

// escapeLike neutralises LIKE's wildcard characters in user-typed search text,
// so a search for "100%" matches that literal text rather than everything.
func escapeLike(text string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(text)
}

// ListSlatedForDeletion returns media whose source has retention_after > 0,
// whose status is downloaded, and whose downloaded_at is set, ordered by
// soonest-to-expire first. The expiration column is the retention cutoff as a
// UNIX timestamp and is exposed on MediaListItem.Expiration so the UI can show
// "in 3 days" / "deleted 2 days ago" deltas.
func (r *MediaRepo) ListSlatedForDeletion(ctx context.Context, limit int) ([]library.MediaListItem, error) {
	query := `SELECT ` + prefixedMediaColumns + `, s.name,
		COALESCE(m.downloaded_at, m.created_at) + s.retention_after_seconds AS expiration
		FROM media m JOIN sources s ON s.id = m.source_id
		WHERE m.status = 'downloaded'
		  AND m.downloaded_at IS NOT NULL
		  AND s.retention_after_seconds > 0
		ORDER BY expiration ASC, m.id ASC`
	if limit > 0 {
		query += ` LIMIT ?`
	}

	args := []any{}
	if limit > 0 {
		args = append(args, limit)
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list slated for deletion: %w", err)
	}
	defer rows.Close()

	var items []library.MediaListItem
	for rows.Next() {
		var expiration int64
		media, sourceName, err := scanMediaWithSource(
			extraScanner{rows: rows, extra: []any{&expiration}},
		)
		if err != nil {
			return nil, fmt.Errorf("store: scan media for deletion: %w", err)
		}
		items = append(items, library.MediaListItem{
			Media:      media,
			SourceName: sourceName,
			Expiration: time.Unix(expiration, 0),
		})
	}
	return items, rows.Err()
}

// SameDayIndex returns the 1-based rank of a media item among the items from the
// same source sharing its upload date, ordered by discovery.
//
// A season-per-year layout numbers episodes by upload date, so two videos posted
// on one day would otherwise be the same episode and a media server would show
// only one of them. Ranking by id keeps a given video's number stable once
// assigned: later uploads get later numbers and never renumber what came before.
func (r *MediaRepo) SameDayIndex(ctx context.Context, id int64) (int, error) {
	var index int
	err := r.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media AS peer
		 WHERE peer.source_id = (SELECT source_id FROM media WHERE id = ?)
		   AND peer.upload_date = (SELECT upload_date FROM media WHERE id = ?)
		   AND peer.id <= ?`, id, id, id).Scan(&index)
	if err != nil {
		return 0, fmt.Errorf("store: same-day index %d: %w", id, err)
	}
	if index < 1 {
		index = 1
	}
	return index, nil
}

// StatsBySource returns the count and total size of downloaded media per source,
// so the UI can say exactly what deleting a source would destroy.
func (r *MediaRepo) StatsBySource(ctx context.Context) (map[int64]library.SourceStats, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT source_id, COUNT(*), COALESCE(SUM(file_size), 0)
		 FROM media WHERE status = ? GROUP BY source_id`, domain.MediaDownloaded)
	if err != nil {
		return nil, fmt.Errorf("store: stats by source: %w", err)
	}
	defer rows.Close()

	stats := make(map[int64]library.SourceStats)
	for rows.Next() {
		var sourceID int64
		var entry library.SourceStats
		if err := rows.Scan(&sourceID, &entry.Files, &entry.Bytes); err != nil {
			return nil, fmt.Errorf("store: scan source stats: %w", err)
		}
		stats[sourceID] = entry
	}
	return stats, rows.Err()
}

// GetWithSource returns one media item together with its source's display name.
func (r *MediaRepo) GetWithSource(ctx context.Context, id int64) (library.MediaListItem, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+prefixedMediaColumns+`, s.name
		 FROM media m JOIN sources s ON s.id = m.source_id
		 WHERE m.id = ?`, id)

	media, sourceName, err := scanMediaWithSource(row)
	if err != nil {
		return library.MediaListItem{}, fmt.Errorf("store: get media with source %d: %w", id, err)
	}
	return library.MediaListItem{Media: media, SourceName: sourceName}, nil
}

// prefixedMediaColumns is mediaColumns qualified with the media table alias "m",
// for the join query in ListWithSource.
const prefixedMediaColumns = `m.id, m.source_id, m.external_id, m.title, m.description,
	m.uploader, m.upload_date, m.duration_seconds, m.is_short, m.is_livestream,
	m.status, m.file_path, m.file_size, m.attempts, m.last_error, m.downloaded_at,
	m.created_at, m.updated_at`

// extraScanner adapts a rowScanner so a helper that scans a fixed column set can
// be reused for a query that selects additional trailing columns. Without this,
// callers are tempted to call Scan twice on the same row - which database/sql
// rejects, and which silently broke every dashboard load.
type extraScanner struct {
	rows  rowScanner
	extra []any
}

func (e extraScanner) Scan(dest ...any) error {
	return e.rows.Scan(append(dest, e.extra...)...)
}

// scanMediaWithSource scans a joined media+source row into a media value and the
// source name. It takes a rowScanner so single-row and iterating callers share
// one scan order.
func scanMediaWithSource(rows rowScanner) (domain.Media, string, error) {
	var (
		media                    domain.Media
		uploadDate, downloadedAt sql.NullInt64
		durationSecs             int64
		isShort, isLivestream    int64
		createdAt, updatedAt     int64
		sourceName               string
	)
	if err := rows.Scan(
		&media.ID, &media.SourceID, &media.ExternalID, &media.Metadata.Title,
		&media.Metadata.Description, &media.Metadata.Uploader, &uploadDate,
		&durationSecs, &isShort, &isLivestream, &media.Status, &media.FilePath,
		&media.FileSize, &media.Attempts, &media.LastError, &downloadedAt,
		&createdAt, &updatedAt, &sourceName,
	); err != nil {
		return domain.Media{}, "", err
	}
	if t := fromNullUnix(uploadDate); t != nil {
		media.Metadata.UploadDate = *t
	}
	media.Metadata.Duration = fromDurationSeconds(durationSecs)
	media.Metadata.IsShort = isShort != 0
	media.Metadata.IsLivestream = isLivestream != 0
	media.DownloadedAt = fromNullUnix(downloadedAt)
	media.CreatedAt = fromUnix(createdAt)
	media.UpdatedAt = fromUnix(updatedAt)
	return media, sourceName, nil
}

// scanMediaList drains a rows cursor into a slice of media.
func scanMediaList(rows *sql.Rows) ([]domain.Media, error) {
	var items []domain.Media
	for rows.Next() {
		media, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan media: %w", err)
		}
		items = append(items, media)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan media: %w", err)
	}
	return items, nil
}

// scanMedia maps a media row into a domain.Media, converting stored integers
// back into times, durations, and booleans.
func scanMedia(row rowScanner) (domain.Media, error) {
	var (
		media                    domain.Media
		uploadDate, downloadedAt sql.NullInt64
		durationSecs             int64
		isShort, isLivestream    int64
		createdAt, updatedAt     int64
	)
	if err := row.Scan(
		&media.ID, &media.SourceID, &media.ExternalID, &media.Metadata.Title,
		&media.Metadata.Description, &media.Metadata.Uploader, &uploadDate,
		&durationSecs, &isShort, &isLivestream, &media.Status, &media.FilePath,
		&media.FileSize, &media.Attempts, &media.LastError, &downloadedAt,
		&createdAt, &updatedAt,
	); err != nil {
		return domain.Media{}, err
	}
	if t := fromNullUnix(uploadDate); t != nil {
		media.Metadata.UploadDate = *t
	}
	media.Metadata.Duration = fromDurationSeconds(durationSecs)
	media.Metadata.IsShort = isShort != 0
	media.Metadata.IsLivestream = isLivestream != 0
	media.DownloadedAt = fromNullUnix(downloadedAt)
	media.CreatedAt = fromUnix(createdAt)
	media.UpdatedAt = fromUnix(updatedAt)
	return media, nil
}

// uploadDatePtr treats a zero upload date as absent so it is stored as NULL,
// keeping "unknown" distinct from the Unix epoch.
func uploadDatePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
