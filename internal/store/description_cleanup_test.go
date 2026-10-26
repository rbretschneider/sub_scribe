package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"sub_scribe/internal/domain"
)

func TestSourceDescriptionCleanup_DefaultsOffAndRoundTrips(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)

	plain := seedSource(t, db, profile.ID, true, nil)
	got, err := db.Sources().Get(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DescriptionCleanup != domain.DescriptionCleanupOff || got.DescriptionStripPatterns != nil {
		t.Errorf("new source = %q %v, want off and no patterns", got.DescriptionCleanup, got.DescriptionStripPatterns)
	}

	got.DescriptionCleanup = domain.DescriptionCleanupOn
	got.DescriptionStripPatterns = []string{`merch`, `(?m)^follow me.*$`}
	if err := db.Sources().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := db.Sources().Get(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, got) {
		t.Errorf("round trip mismatch:\n got=%+v\nwant=%+v", again, got)
	}

	again.DescriptionCleanup = domain.DescriptionCleanupOff
	again.DescriptionStripPatterns = nil
	if err := db.Sources().Update(ctx, again); err != nil {
		t.Fatal(err)
	}
	cleared, _ := db.Sources().Get(ctx, plain.ID)
	if cleared.DescriptionCleanup != domain.DescriptionCleanupOff || len(cleared.DescriptionStripPatterns) != 0 {
		t.Errorf("after clearing = %q %v", cleared.DescriptionCleanup, cleared.DescriptionStripPatterns)
	}
}

func TestSourceDescriptionCleanup_MalformedPatternsReadAsEmpty(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	src := seedSource(t, db, seedProfile(t, db).ID, true, nil)
	if _, err := db.sql.Exec(`UPDATE sources SET description_strip_patterns = 'not json' WHERE id = ?`, src.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.Sources().Get(ctx, src.ID)
	if err != nil {
		t.Fatalf("a hand-edited value must not make the source unreadable: %v", err)
	}
	if len(got.DescriptionStripPatterns) != 0 {
		t.Errorf("patterns = %v, want none", got.DescriptionStripPatterns)
	}
}

func TestDescriptionMigrations_UpgradeFromVersion22KeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open(driverName, "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	old := &DB{sql: raw}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version <= 22 {
			if err := old.applyMigration(m); err != nil {
				t.Fatalf("apply %d: %v", m.version, err)
			}
		}
	}
	// Rows written by the previous release, using only the version 22 columns.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO media_profiles(name, output_path_template, kind, quality_format, metadata_format, created_at, updated_at)
		VALUES('p', '{title}', 'video', 'best', 'episode', 1, 1)`)
	mustExec(`INSERT INTO sources(name, url, collection_type, media_profile_id, feed_token, created_at, updated_at)
		VALUES('s', 'https://youtube.com/@s', 'channel', 1, 'tok', 1, 1)`)
	mustExec(`INSERT INTO media(source_id, external_id, description, uploader, created_at, updated_at)
		VALUES(1, 'v1', 'hello', 'Chan', 1, 1)`)
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 22 database: %v", err)
	}
	defer db.Close()
	src, err := db.Sources().Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if src.DescriptionCleanup != domain.DescriptionCleanupOff || len(src.DescriptionStripPatterns) != 0 {
		t.Errorf("migrated source = %q %v, want off and no patterns", src.DescriptionCleanup, src.DescriptionStripPatterns)
	}
	n, err := db.Media().CountDescriptionsByUploader(context.Background(), "Chan")
	if err != nil || n != 1 {
		t.Errorf("count = %d, %v; want the existing row", n, err)
	}
	db.Close()

	// Opening a second time applies nothing and does not fail.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	again.Close()
}

func TestDescriptionMigrations_FreshDatabaseHasColumnsAndIndex(t *testing.T) {
	db := openTestDB(t)
	var idx int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_media_uploader'`).Scan(&idx); err != nil || idx != 1 {
		t.Errorf("idx_media_uploader present = %d, %v; want 1", idx, err)
	}
	var cleanup, patterns string
	if _, err := db.sql.Exec(`INSERT INTO media_profiles(name, output_path_template, kind, quality_format, metadata_format, created_at, updated_at)
		VALUES('p', '{title}', 'video', 'best', 'episode', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO sources(name, url, collection_type, media_profile_id, feed_token, created_at, updated_at)
		VALUES('s', 'u', 'channel', 1, 't', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT description_cleanup, description_strip_patterns FROM sources`).Scan(&cleanup, &patterns); err != nil {
		t.Fatal(err)
	}
	if cleanup != "off" || patterns != "[]" {
		t.Errorf("column defaults = %q, %q; want off and []", cleanup, patterns)
	}
}

func TestChannelHistory_CountsAcrossSourcesAndIgnoresEmpty(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	a := seedSource(t, db, profile.ID, true, nil)
	b := seedSource(t, db, profile.ID, true, nil)

	add := func(sourceID int64, ext, uploader, desc string) {
		t.Helper()
		m := newMedia(sourceID, ext, domain.MediaPending)
		m.Metadata.Uploader = uploader
		m.Metadata.Description = desc
		if _, err := db.Media().Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	add(a.ID, "a1", "Chan", "one")
	add(a.ID, "a2", "Chan", "two")
	add(b.ID, "b1", "Chan", "three")
	add(b.ID, "b2", "Chan", "")
	add(b.ID, "b3", "Other", "four")

	n, err := db.Media().CountDescriptionsByUploader(ctx, "Chan")
	if err != nil || n != 3 {
		t.Fatalf("count = %d, %v; want 3 (two sources, empty ignored)", n, err)
	}
	got, err := db.Media().DescriptionsByUploader(ctx, "Chan")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("descriptions = %v, want %v", got, want)
	}
	none, err := db.Media().DescriptionsByUploader(ctx, "Nobody")
	if err != nil || len(none) != 0 {
		t.Errorf("unknown uploader = %v, %v; want none", none, err)
	}
}
