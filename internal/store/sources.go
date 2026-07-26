package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// SourceRepo satisfies library.SourceRepo.
var _ library.SourceRepo = (*SourceRepo)(nil)

// SourceRepo persists sources: the remote collections sub_scribe tracks. It maps
// domain.Source to and from the sources table using only parameterized queries.
type SourceRepo struct {
	sql *sql.DB
}

// sourceColumns is the shared SELECT/RETURNING column list, kept in one place so
// the scan order and the queries can never drift apart.
const sourceColumns = `id, name, url, collection_type, media_profile_id, enabled,
	index_frequency_seconds, last_indexed_at, cookie_behavior, download_cutoff,
	title_filter_pattern, shorts_rule, livestreams_rule, retention_after_seconds,
	cutoff_window_seconds, created_at, updated_at`

// toDurationSeconds converts a duration to the whole-seconds integer stored in
// the *_seconds columns.
func toDurationSeconds(d time.Duration) int64 {
	return int64(d / time.Second)
}

// fromDurationSeconds converts a whole-seconds column back into a duration.
func fromDurationSeconds(secs int64) time.Duration {
	return time.Duration(secs) * time.Second
}

// Create inserts a new source and returns its assigned id.
func (r *SourceRepo) Create(ctx context.Context, source domain.Source) (int64, error) {
	res, err := r.sql.ExecContext(ctx,
		`INSERT INTO sources(name, url, collection_type, media_profile_id, enabled,
			index_frequency_seconds, last_indexed_at, cookie_behavior, download_cutoff,
			title_filter_pattern, shorts_rule, livestreams_rule, retention_after_seconds,
			cutoff_window_seconds, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source.Name, source.URL, source.CollectionType, source.MediaProfileID,
		boolToInt(source.Enabled), toDurationSeconds(source.IndexFrequency),
		toNullUnix(source.LastIndexedAt), source.CookieBehavior,
		toNullUnix(source.DownloadCutoff), source.TitleFilterPattern,
		source.ShortsRule, source.LivestreamsRule,
		toDurationSeconds(source.RetentionAfter), toDurationSeconds(source.CutoffWindow),
		source.CreatedAt.Unix(), source.UpdatedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: create source: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create source id: %w", err)
	}
	return id, nil
}

// Get returns the source with the given id.
func (r *SourceRepo) Get(ctx context.Context, id int64) (domain.Source, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id)
	source, err := scanSource(row)
	if err != nil {
		return domain.Source{}, fmt.Errorf("store: get source %d: %w", id, err)
	}
	return source, nil
}

// List returns every source ordered by id.
func (r *SourceRepo) List(ctx context.Context) ([]domain.Source, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT `+sourceColumns+` FROM sources ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list sources: %w", err)
	}
	defer rows.Close()

	var sources []domain.Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list sources: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list sources: %w", err)
	}
	return sources, nil
}

// Update writes every mutable field of an existing source.
func (r *SourceRepo) Update(ctx context.Context, source domain.Source) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE sources SET name = ?, url = ?, collection_type = ?, media_profile_id = ?,
			enabled = ?, index_frequency_seconds = ?, last_indexed_at = ?, cookie_behavior = ?,
			download_cutoff = ?, title_filter_pattern = ?, shorts_rule = ?, livestreams_rule = ?,
			retention_after_seconds = ?, cutoff_window_seconds = ?, updated_at = ?
		 WHERE id = ?`,
		source.Name, source.URL, source.CollectionType, source.MediaProfileID,
		boolToInt(source.Enabled), toDurationSeconds(source.IndexFrequency),
		toNullUnix(source.LastIndexedAt), source.CookieBehavior,
		toNullUnix(source.DownloadCutoff), source.TitleFilterPattern,
		source.ShortsRule, source.LivestreamsRule,
		toDurationSeconds(source.RetentionAfter), toDurationSeconds(source.CutoffWindow),
		source.UpdatedAt.Unix(),
		source.ID,
	); err != nil {
		return fmt.Errorf("store: update source %d: %w", source.ID, err)
	}
	return nil
}

// Delete removes a source (and, via ON DELETE CASCADE, its media).
func (r *SourceRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.sql.ExecContext(ctx,
		`DELETE FROM sources WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete source %d: %w", id, err)
	}
	return nil
}

// DueForIndex returns enabled sources that have never been indexed or whose
// index interval has elapsed as of now, ordered by id.
func (r *SourceRepo) DueForIndex(ctx context.Context, now time.Time) ([]domain.Source, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT `+sourceColumns+` FROM sources
		 WHERE enabled = 1
		   AND (last_indexed_at IS NULL OR last_indexed_at + index_frequency_seconds <= ?)
		 ORDER BY id ASC`,
		now.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: due for index: %w", err)
	}
	defer rows.Close()

	var sources []domain.Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("store: due for index: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: due for index: %w", err)
	}
	return sources, nil
}

// MarkIndexed records that a source was scanned at now.
func (r *SourceRepo) MarkIndexed(ctx context.Context, id int64, now time.Time) error {
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE sources SET last_indexed_at = ?, updated_at = ? WHERE id = ?`,
		now.Unix(), now.Unix(), id,
	); err != nil {
		return fmt.Errorf("store: mark indexed %d: %w", id, err)
	}
	return nil
}

// scanSource maps a source row into a domain.Source, converting stored integers
// back into times, durations, and booleans.
func scanSource(row rowScanner) (domain.Source, error) {
	var (
		source                        domain.Source
		enabled, indexFreq, retention int64
		cutoffWindow                  int64
		lastIndexed, downloadCutoff   sql.NullInt64
		createdAt, updatedAt          int64
	)
	if err := row.Scan(
		&source.ID, &source.Name, &source.URL, &source.CollectionType,
		&source.MediaProfileID, &enabled, &indexFreq, &lastIndexed,
		&source.CookieBehavior, &downloadCutoff, &source.TitleFilterPattern,
		&source.ShortsRule, &source.LivestreamsRule, &retention,
		&cutoffWindow, &createdAt, &updatedAt,
	); err != nil {
		return domain.Source{}, err
	}
	source.Enabled = enabled != 0
	source.IndexFrequency = fromDurationSeconds(indexFreq)
	source.LastIndexedAt = fromNullUnix(lastIndexed)
	source.DownloadCutoff = fromNullUnix(downloadCutoff)
	source.CutoffWindow = fromDurationSeconds(cutoffWindow)
	source.RetentionAfter = fromDurationSeconds(retention)
	source.CreatedAt = fromUnix(createdAt)
	source.UpdatedAt = fromUnix(updatedAt)
	return source, nil
}

// boolToInt maps a Go bool to the 0/1 integer stored in boolean columns.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
