package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// ProfileRepo satisfies library.ProfileRepo.
var _ library.ProfileRepo = (*ProfileRepo)(nil)

// ProfileRepo persists media profiles: the reusable "how and where to download"
// settings shared across sources. List-valued fields are stored as JSON arrays.
type ProfileRepo struct {
	sql *sql.DB
}

// profileColumns is the shared SELECT/RETURNING column list, kept in one place so
// the scan order and the queries can never drift apart.
const profileColumns = `id, name, output_path_template, kind, quality_format,
	embed_metadata, embed_thumbnail, embed_subtitles, subtitle_languages,
	sponsorblock_mode, sponsorblock_categories, redownload_after_seconds,
	metadata_format, extra_ytdlp_args, post_download_command, write_thumbnail,
	created_at, updated_at`

// Create inserts a new media profile and returns its assigned id.
func (r *ProfileRepo) Create(ctx context.Context, profile domain.MediaProfile) (int64, error) {
	subs, err := marshalJSONList(profile.SubtitleLanguages)
	if err != nil {
		return 0, fmt.Errorf("store: create profile: %w", err)
	}
	cats, err := marshalJSONList(profile.SponsorBlockCategories)
	if err != nil {
		return 0, fmt.Errorf("store: create profile: %w", err)
	}
	extraArgs, err := marshalJSONList(profile.ExtraYtdlpArgs)
	if err != nil {
		return 0, fmt.Errorf("store: create profile: %w", err)
	}
	res, err := r.sql.ExecContext(ctx,
		`INSERT INTO media_profiles(name, output_path_template, kind, quality_format,
			embed_metadata, embed_thumbnail, embed_subtitles, subtitle_languages,
			sponsorblock_mode, sponsorblock_categories, redownload_after_seconds,
			metadata_format, extra_ytdlp_args, post_download_command, write_thumbnail,
			created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.Name, profile.OutputPathTemplate, profile.Kind, profile.QualityFormat,
		boolToInt(profile.EmbedMetadata), boolToInt(profile.EmbedThumbnail),
		boolToInt(profile.EmbedSubtitles), subs, profile.SponsorBlockMode, cats,
		toDurationSeconds(profile.RedownloadAfter), profile.MetadataFormat,
		extraArgs, profile.PostDownloadCommand, boolToInt(profile.WriteThumbnail),
		profile.CreatedAt.Unix(), profile.UpdatedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: create profile: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create profile id: %w", err)
	}
	return id, nil
}

// Get returns the media profile with the given id.
func (r *ProfileRepo) Get(ctx context.Context, id int64) (domain.MediaProfile, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+profileColumns+` FROM media_profiles WHERE id = ?`, id)
	profile, err := scanProfile(row)
	if err != nil {
		return domain.MediaProfile{}, fmt.Errorf("store: get profile %d: %w", id, err)
	}
	return profile, nil
}

// List returns every media profile ordered by id.
func (r *ProfileRepo) List(ctx context.Context) ([]domain.MediaProfile, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT `+profileColumns+` FROM media_profiles ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list profiles: %w", err)
	}
	defer rows.Close()

	var profiles []domain.MediaProfile
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list profiles: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list profiles: %w", err)
	}
	return profiles, nil
}

// Update writes every mutable field of an existing media profile.
func (r *ProfileRepo) Update(ctx context.Context, profile domain.MediaProfile) error {
	subs, err := marshalJSONList(profile.SubtitleLanguages)
	if err != nil {
		return fmt.Errorf("store: update profile %d: %w", profile.ID, err)
	}
	cats, err := marshalJSONList(profile.SponsorBlockCategories)
	if err != nil {
		return fmt.Errorf("store: update profile %d: %w", profile.ID, err)
	}
	extraArgs, err := marshalJSONList(profile.ExtraYtdlpArgs)
	if err != nil {
		return fmt.Errorf("store: update profile %d: %w", profile.ID, err)
	}
	if _, err := r.sql.ExecContext(ctx,
		`UPDATE media_profiles SET name = ?, output_path_template = ?, kind = ?,
			quality_format = ?, embed_metadata = ?, embed_thumbnail = ?, embed_subtitles = ?,
			subtitle_languages = ?, sponsorblock_mode = ?, sponsorblock_categories = ?,
			redownload_after_seconds = ?, metadata_format = ?, extra_ytdlp_args = ?,
			post_download_command = ?, write_thumbnail = ?, updated_at = ?
		 WHERE id = ?`,
		profile.Name, profile.OutputPathTemplate, profile.Kind, profile.QualityFormat,
		boolToInt(profile.EmbedMetadata), boolToInt(profile.EmbedThumbnail),
		boolToInt(profile.EmbedSubtitles), subs, profile.SponsorBlockMode, cats,
		toDurationSeconds(profile.RedownloadAfter), profile.MetadataFormat,
		extraArgs, profile.PostDownloadCommand, boolToInt(profile.WriteThumbnail),
		profile.UpdatedAt.Unix(),
		profile.ID,
	); err != nil {
		return fmt.Errorf("store: update profile %d: %w", profile.ID, err)
	}
	return nil
}

// Delete removes a media profile.
func (r *ProfileRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.sql.ExecContext(ctx,
		`DELETE FROM media_profiles WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete profile %d: %w", id, err)
	}
	return nil
}

// scanProfile maps a media profile row into a domain.MediaProfile, decoding JSON
// list columns and converting stored integers back into durations and booleans.
func scanProfile(row rowScanner) (domain.MediaProfile, error) {
	var (
		profile                          domain.MediaProfile
		embedMeta, embedThumb, embedSubs int64
		writeThumb                       int64
		subs, cats, extraArgs            string
		redownloadSecs                   int64
		createdAt, updatedAt             int64
	)
	if err := row.Scan(
		&profile.ID, &profile.Name, &profile.OutputPathTemplate, &profile.Kind,
		&profile.QualityFormat, &embedMeta, &embedThumb, &embedSubs, &subs,
		&profile.SponsorBlockMode, &cats, &redownloadSecs, &profile.MetadataFormat,
		&extraArgs, &profile.PostDownloadCommand, &writeThumb, &createdAt, &updatedAt,
	); err != nil {
		return domain.MediaProfile{}, err
	}
	languages, err := unmarshalJSONList[string](subs)
	if err != nil {
		return domain.MediaProfile{}, err
	}
	categories, err := unmarshalJSONList[domain.SponsorBlockCategory](cats)
	if err != nil {
		return domain.MediaProfile{}, err
	}
	extra, err := unmarshalJSONList[string](extraArgs)
	if err != nil {
		return domain.MediaProfile{}, err
	}
	profile.EmbedMetadata = embedMeta != 0
	profile.EmbedThumbnail = embedThumb != 0
	profile.WriteThumbnail = writeThumb != 0
	profile.EmbedSubtitles = embedSubs != 0
	profile.SubtitleLanguages = languages
	profile.SponsorBlockCategories = categories
	profile.ExtraYtdlpArgs = extra
	profile.RedownloadAfter = fromDurationSeconds(redownloadSecs)
	profile.CreatedAt = fromUnix(createdAt)
	profile.UpdatedAt = fromUnix(updatedAt)
	return profile, nil
}

// marshalJSONList encodes a list column, normalizing an empty list to "[]" so the
// NOT NULL, default-'[]' columns never see a JSON "null".
func marshalJSONList[T any](list []T) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("marshal json list: %w", err)
	}
	return string(b), nil
}

// unmarshalJSONList decodes a JSON array column into a typed slice.
func unmarshalJSONList[T any](s string) ([]T, error) {
	if s == "" {
		return nil, nil
	}
	var list []T
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil, fmt.Errorf("unmarshal json list: %w", err)
	}
	return list, nil
}
