package store

import (
	"database/sql"
	"fmt"

	"sub_scribe/internal/domain"
)

// migration is a single, ordered, forward-only schema change. Migrations are
// applied in slice order inside a transaction and recorded so each runs once.
// A migration that needs values SQL cannot produce (random secrets, per-row
// generated data) supplies a run function, executed after stmt in the same
// transaction.
type migration struct {
	version int
	name    string
	stmt    string
	run     func(tx *sql.Tx) error
}

// migrations is the ordered schema history. Append new migrations; never edit or
// reorder applied ones, so existing databases upgrade cleanly.
var migrations = []migration{
	{
		version: 1,
		name:    "create_media_profiles",
		stmt: `CREATE TABLE media_profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			output_path_template TEXT NOT NULL,
			kind TEXT NOT NULL,
			quality_format TEXT NOT NULL DEFAULT '',
			embed_metadata INTEGER NOT NULL DEFAULT 0,
			embed_thumbnail INTEGER NOT NULL DEFAULT 0,
			embed_subtitles INTEGER NOT NULL DEFAULT 0,
			subtitle_languages TEXT NOT NULL DEFAULT '[]',
			sponsorblock_mode TEXT NOT NULL DEFAULT 'off',
			sponsorblock_categories TEXT NOT NULL DEFAULT '[]',
			redownload_after_seconds INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	{
		version: 2,
		name:    "create_sources",
		stmt: `CREATE TABLE sources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			collection_type TEXT NOT NULL,
			media_profile_id INTEGER NOT NULL REFERENCES media_profiles(id),
			enabled INTEGER NOT NULL DEFAULT 1,
			index_frequency_seconds INTEGER NOT NULL DEFAULT 21600,
			last_indexed_at INTEGER,
			cookie_behavior TEXT NOT NULL DEFAULT 'when_needed',
			download_cutoff INTEGER,
			title_filter_pattern TEXT NOT NULL DEFAULT '',
			shorts_rule TEXT NOT NULL DEFAULT 'include',
			livestreams_rule TEXT NOT NULL DEFAULT 'include',
			retention_after_seconds INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	{
		version: 3,
		name:    "create_media",
		stmt: `CREATE TABLE media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			external_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			uploader TEXT NOT NULL DEFAULT '',
			upload_date INTEGER,
			duration_seconds INTEGER NOT NULL DEFAULT 0,
			is_short INTEGER NOT NULL DEFAULT 0,
			is_livestream INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			file_path TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			downloaded_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(source_id, external_id)
		)`,
	},
	{
		version: 4,
		name:    "create_tasks",
		stmt: `CREATE TABLE tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE,
			media_id INTEGER REFERENCES media(id) ON DELETE CASCADE,
			priority INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			last_error TEXT NOT NULL DEFAULT '',
			run_after INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	{
		version: 5,
		name:    "index_tasks_queue",
		stmt: `CREATE INDEX idx_tasks_claim
			ON tasks(status, run_after, priority DESC, id)`,
	},
	{
		version: 6,
		name:    "index_media_status",
		stmt:    `CREATE INDEX idx_media_source_status ON media(source_id, status)`,
	},
	{
		version: 7,
		name:    "add_metadata_format",
		stmt: `ALTER TABLE media_profiles
			ADD COLUMN metadata_format TEXT NOT NULL DEFAULT 'plex'`,
	},
	{
		version: 8,
		name:    "add_extra_ytdlp_args",
		stmt: `ALTER TABLE media_profiles
			ADD COLUMN extra_ytdlp_args TEXT NOT NULL DEFAULT '[]'`,
	},
	{
		version: 9,
		name:    "add_post_download_command",
		stmt: `ALTER TABLE media_profiles
			ADD COLUMN post_download_command TEXT NOT NULL DEFAULT ''`,
	},
	{
		version: 10,
		name:    "add_cutoff_window",
		stmt: `ALTER TABLE sources
			ADD COLUMN cutoff_window_seconds INTEGER NOT NULL DEFAULT 0`,
	},
	{
		version: 11,
		name:    "add_write_thumbnail",
		// Defaults on, because a sidecar thumbnail is what media servers read as
		// artwork and existing profiles should gain it without being re-edited.
		stmt: `ALTER TABLE media_profiles
			ADD COLUMN write_thumbnail INTEGER NOT NULL DEFAULT 1`,
	},
	{
		version: 12,
		name:    "index_media_status",
		// The dashboard counts and the library's status filters query by status
		// alone. idx_media_source_status leads with source_id, so it cannot serve
		// them and every one of those became a full table scan.
		stmt: `CREATE INDEX idx_media_status ON media(status)`,
	},
	{
		version: 13,
		name:    "index_media_recency",
		// The library lists newest-first. Without this the whole table is sorted on
		// every page load, which grows with the archive rather than the page size.
		stmt: `CREATE INDEX idx_media_recency
			ON media(COALESCE(downloaded_at, created_at) DESC, id DESC)`,
	},
	{
		version: 14,
		name:    "index_tasks_recency",
		// Same for the jobs screen, which orders by most recent activity.
		stmt: `CREATE INDEX idx_tasks_updated ON tasks(updated_at DESC, id DESC)`,
	},
	{
		version: 15,
		name:    "metadata_format_describes_layout",
		// The sidecar shape has to match the folder layout, not a brand. Both old
		// values become "episode" because the layout every profile produces is
		// season-based: the "plex" option had been writing movie metadata into a
		// season-based tree, which reads badly everywhere and which Plex ignored
		// entirely. A flat layout can opt into "movie" explicitly.
		stmt: `UPDATE media_profiles
			SET metadata_format = 'episode'
			WHERE metadata_format IN ('plex', 'jellyfin')`,
	},
	{
		version: 16,
		name:    "season_episode_naming_default",
		// The previous default put a plain date in the filename, which media
		// servers cannot turn into an episode: Plex matches the channel against its
		// TV database, fails, and shows invented names like "Episode 04-22". The
		// season/episode token fixes that.
		//
		// Only profiles still on the exact old default are rewritten — a template
		// the user edited is theirs. Files already downloaded under the old name no
		// longer match the new one and will be fetched again; startup recovery
		// adopts anything that does still match.
		stmt: `UPDATE media_profiles
			SET output_path_template =
				'{{ source_name }}/Season {{ upload_year }}/{{ season_episode }} - {{ title }}'
			WHERE output_path_template =
				'{{ source_name }}/Season {{ upload_year }}/{{ source_name }} - {{ upload_date }} - {{ title }} [{{ id }}]'`,
	},
	{
		version: 17,
		name:    "sponsorblock_categories_are_explicit",
		// An empty category list used to mean "cut sponsor, self-promotion, and
		// interaction segments", while the profile screen showed nothing ticked.
		// The builder no longer invents that set, so a profile left empty would
		// silently stop cutting anything at all — the opposite surprise.
		//
		// Writing the one category worth cutting by default settles it in both
		// directions: sponsors are still removed, the screen now says so, and the
		// two subjective categories that could take a chunk of the actual episode
		// are gone unless someone asks for them.
		stmt: `UPDATE media_profiles
			SET sponsorblock_categories = '["sponsor"]'
			WHERE sponsorblock_mode != 'off'
			  AND sponsorblock_categories IN ('[]', '', 'null')`,
	},
	{
		version: 18,
		name:    "add_source_feed_tokens",
		// The feed token is the per-source secret that lets podcast apps — which
		// cannot complete a browser login — fetch /feeds/{id}?t=<token> when the
		// UI is otherwise locked. Existing sources are backfilled so every feed
		// URL is tokenized the moment auth turns on, not only newly added ones.
		stmt: `ALTER TABLE sources
			ADD COLUMN feed_token TEXT NOT NULL DEFAULT ''`,
		run: backfillFeedTokens,
	},
	{
		version: 19,
		name:    "create_settings",
		// A key-value table for app-generated runtime state that must survive
		// restarts but should never burden deployment as an env var — the first
		// occupant is the session-cookie signing secret.
		stmt: `CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	},
}

// backfillFeedTokens assigns a fresh random token to every source created
// before feed tokens existed. SQL cannot draw from crypto/rand, so the rows
// are updated one by one inside the migration's transaction.
func backfillFeedTokens(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id FROM sources WHERE feed_token = ''`)
	if err != nil {
		return fmt.Errorf("list sources without tokens: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan source id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list sources without tokens: %w", err)
	}

	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE sources SET feed_token = ? WHERE id = ?`,
			domain.NewFeedToken(), id); err != nil {
			return fmt.Errorf("backfill feed token for source %d: %w", id, err)
		}
	}
	return nil
}

// migrate applies every migration not yet recorded, each in its own transaction,
// advancing the schema to the latest version.
func (db *DB) migrate() error {
	if _, err := db.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := db.appliedVersions()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := db.applyMigration(m); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// appliedVersions returns the set of migration versions already recorded.
func (db *DB) appliedVersions() (map[int]bool, error) {
	rows, err := db.sql.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan migration version: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

// applyMigration runs one migration and records it atomically.
func (db *DB) applyMigration(m migration) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.stmt); err != nil {
		return fmt.Errorf("exec statement: %w", err)
	}
	if m.run != nil {
		if err := m.run(tx); err != nil {
			return fmt.Errorf("run migration code: %w", err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, unixepoch())`,
		m.version, m.name,
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return tx.Commit()
}
