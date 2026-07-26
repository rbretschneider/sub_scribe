package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

// openTestDB opens a fresh temp-file database for a test.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// unixTime returns a UTC time truncated to whole seconds, matching storage.
func unixTime(secs int64) time.Time {
	return time.Unix(secs, 0).UTC()
}

// seedProfile inserts a fully-populated profile and returns it with its id set.
func seedProfile(t *testing.T, db *DB) domain.MediaProfile {
	t.Helper()
	ctx := context.Background()
	profile := domain.MediaProfile{
		Name:               "1080p Plex",
		OutputPathTemplate: "{uploader}/{title}",
		Kind:               domain.MediaVideo,
		QualityFormat:      "bestvideo[height<=1080]+bestaudio",
		MetadataFormat:     domain.MetadataMovie,
		EmbedMetadata:      true,
		EmbedThumbnail:     true,
		EmbedSubtitles:     true,
		SubtitleLanguages:  []string{"en", "es"},
		SponsorBlockMode:   domain.SponsorBlockRemove,
		SponsorBlockCategories: []domain.SponsorBlockCategory{
			domain.SponsorBlockSponsor, domain.SponsorBlockIntro,
		},
		RedownloadAfter:     48 * time.Hour,
		ExtraYtdlpArgs:      []string{"--concurrent-fragments", "4"},
		PostDownloadCommand: "/config/scripts/post.sh",
		CreatedAt:           unixTime(1_700_000_000),
		UpdatedAt:           unixTime(1_700_000_000),
	}
	id, err := db.Profiles().Create(ctx, profile)
	if err != nil {
		t.Fatalf("Create profile: %v", err)
	}
	profile.ID = id
	return profile
}

// seedSource inserts a source referencing profileID and returns it with its id.
func seedSource(t *testing.T, db *DB, profileID int64, enabled bool, lastIndexed *time.Time) domain.Source {
	t.Helper()
	ctx := context.Background()
	cutoff := unixTime(1_600_000_000)
	source := domain.Source{
		Name:               "Some Channel",
		URL:                "https://youtube.com/@some",
		CollectionType:     domain.CollectionChannel,
		MediaProfileID:     profileID,
		Enabled:            enabled,
		IndexFrequency:     6 * time.Hour,
		LastIndexedAt:      lastIndexed,
		CookieBehavior:     domain.CookieWhenNeeded,
		DownloadCutoff:     &cutoff,
		TitleFilterPattern: "^Episode",
		ShortsRule:         domain.InclusionExclude,
		LivestreamsRule:    domain.InclusionInclude,
		RetentionAfter:     30 * 24 * time.Hour,
		CutoffWindow:       365 * 24 * time.Hour,
		CreatedAt:          unixTime(1_700_000_100),
		UpdatedAt:          unixTime(1_700_000_100),
	}
	id, err := db.Sources().Create(ctx, source)
	if err != nil {
		t.Fatalf("Create source: %v", err)
	}
	source.ID = id
	return source
}

func TestProfileRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	want := seedProfile(t, db)

	got, err := db.Profiles().Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get profile: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("profile round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	// Update mutates every field including the JSON lists and duration.
	want.Name = "720p Audio"
	want.Kind = domain.MediaAudio
	want.MetadataFormat = domain.MetadataEpisode
	want.ExtraYtdlpArgs = []string{"--force-ipv4"}
	want.PostDownloadCommand = ""
	want.SubtitleLanguages = []string{"fr"}
	want.SponsorBlockCategories = nil
	want.SponsorBlockMode = domain.SponsorBlockOff
	want.RedownloadAfter = 0
	want.UpdatedAt = unixTime(1_700_000_500)
	if err := db.Profiles().Update(ctx, want); err != nil {
		t.Fatalf("Update profile: %v", err)
	}
	got, err = db.Profiles().Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	// Empty categories come back as a nil slice; normalize for comparison.
	if len(got.SponsorBlockCategories) != 0 {
		t.Errorf("expected no categories, got %v", got.SponsorBlockCategories)
	}
	got.SponsorBlockCategories = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("profile update mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	list, err := db.Profiles().List(ctx)
	if err != nil {
		t.Fatalf("List profiles: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(list))
	}

	if err := db.Profiles().Delete(ctx, want.ID); err != nil {
		t.Fatalf("Delete profile: %v", err)
	}
	if list, err = db.Profiles().List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("expected empty after delete, got len=%d err=%v", len(list), err)
	}
}

func TestSourceRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	want := seedSource(t, db, profile.ID, true, nil)

	got, err := db.Sources().Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get source: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("source round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	indexed := unixTime(1_700_100_000)
	want.Name = "Renamed"
	want.Enabled = false
	want.IndexFrequency = 12 * time.Hour
	want.LastIndexedAt = &indexed
	want.DownloadCutoff = nil
	want.CutoffWindow = 0
	want.RetentionAfter = 0
	want.UpdatedAt = unixTime(1_700_100_100)
	if err := db.Sources().Update(ctx, want); err != nil {
		t.Fatalf("Update source: %v", err)
	}
	got, err = db.Sources().Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("source update mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	if err := db.Sources().Delete(ctx, want.ID); err != nil {
		t.Fatalf("Delete source: %v", err)
	}
	list, err := db.Sources().List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("expected empty after delete, got len=%d err=%v", len(list), err)
	}
}

func TestSourceDueForIndex(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	now := unixTime(1_700_500_000)

	never := seedSource(t, db, profile.ID, true, nil)
	staleAt := now.Add(-7 * time.Hour) // freq is 6h -> due
	stale := seedSource(t, db, profile.ID, true, &staleAt)
	freshAt := now.Add(-1 * time.Hour) // not yet due
	seedSource(t, db, profile.ID, true, &freshAt)
	disabledAt := now.Add(-48 * time.Hour)
	seedSource(t, db, profile.ID, false, &disabledAt) // stale but disabled

	due, err := db.Sources().DueForIndex(ctx, now)
	if err != nil {
		t.Fatalf("DueForIndex: %v", err)
	}
	gotIDs := map[int64]bool{}
	for _, s := range due {
		gotIDs[s.ID] = true
	}
	if !gotIDs[never.ID] {
		t.Errorf("never-indexed source %d should be due", never.ID)
	}
	if !gotIDs[stale.ID] {
		t.Errorf("stale source %d should be due", stale.ID)
	}
	if len(due) != 2 {
		t.Errorf("expected exactly 2 due, got %d (%v)", len(due), gotIDs)
	}
}

func TestSourceMarkIndexed(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	src := seedSource(t, db, profile.ID, true, nil)

	when := unixTime(1_700_600_000)
	if err := db.Sources().MarkIndexed(ctx, src.ID, when); err != nil {
		t.Fatalf("MarkIndexed: %v", err)
	}
	got, err := db.Sources().Get(ctx, src.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastIndexedAt == nil || !got.LastIndexedAt.Equal(when) {
		t.Errorf("LastIndexedAt = %v, want %v", got.LastIndexedAt, when)
	}
}

// newMedia builds a media value for a source with a given external id.
func newMedia(sourceID int64, externalID string, status domain.MediaStatus) domain.Media {
	return domain.Media{
		SourceID:   sourceID,
		ExternalID: externalID,
		Metadata: domain.MediaMetadata{
			Title:        "My Title",
			Description:  "desc",
			Uploader:     "Uploader",
			UploadDate:   unixTime(1_650_000_000),
			Duration:     20 * time.Minute,
			IsShort:      false,
			IsLivestream: true,
		},
		Status:    status,
		CreatedAt: unixTime(1_700_000_200),
		UpdatedAt: unixTime(1_700_000_200),
	}
}

func TestMediaRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	src := seedSource(t, db, profile.ID, true, nil)

	want := newMedia(src.ID, "vid123", domain.MediaPending)
	id, err := db.Media().Upsert(ctx, want)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	want.ID = id

	got, err := db.Media().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get media: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("media round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	exists, err := db.Media().ExistsBySource(ctx, src.ID, "vid123")
	if err != nil || !exists {
		t.Errorf("ExistsBySource = %v, %v; want true, nil", exists, err)
	}
	missing, err := db.Media().ExistsBySource(ctx, src.ID, "nope")
	if err != nil || missing {
		t.Errorf("ExistsBySource(nope) = %v, %v; want false, nil", missing, err)
	}
}

func TestMediaUpsertDedupesAndPreservesProgress(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	src := seedSource(t, db, profile.ID, true, nil)

	first := newMedia(src.ID, "same", domain.MediaPending)
	id1, err := db.Media().Upsert(ctx, first)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Simulate a completed download before re-indexing.
	dlTime := unixTime(1_700_050_000)
	if err := db.Media().MarkDownloaded(ctx, id1, "/media/file.mp4", 4096, dlTime); err != nil {
		t.Fatalf("MarkDownloaded: %v", err)
	}

	// Re-index: same external id, refreshed metadata, but pending status again.
	second := newMedia(src.ID, "same", domain.MediaPending)
	second.Metadata.Title = "Updated Title"
	second.UpdatedAt = unixTime(1_700_090_000)
	id2, err := db.Media().Upsert(ctx, second)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("Upsert created a duplicate: id1=%d id2=%d", id1, id2)
	}

	list, err := db.Media().ListBySource(ctx, src.ID)
	if err != nil {
		t.Fatalf("ListBySource: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 row after re-upsert, got %d", len(list))
	}
	got := list[0]
	if got.Metadata.Title != "Updated Title" {
		t.Errorf("metadata not refreshed: title=%q", got.Metadata.Title)
	}
	if got.Status != domain.MediaDownloaded {
		t.Errorf("status overwritten: got %q, want downloaded", got.Status)
	}
	if got.FilePath != "/media/file.mp4" || got.FileSize != 4096 {
		t.Errorf("download progress overwritten: path=%q size=%d", got.FilePath, got.FileSize)
	}
	if got.DownloadedAt == nil || !got.DownloadedAt.Equal(dlTime) {
		t.Errorf("downloaded_at overwritten: %v", got.DownloadedAt)
	}
}

func TestMediaMarkDownloadedAndSetError(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	src := seedSource(t, db, profile.ID, true, nil)

	id, err := db.Media().Upsert(ctx, newMedia(src.ID, "a", domain.MediaPending))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	when := unixTime(1_700_070_000)
	if err := db.Media().SetStatus(ctx, id, domain.MediaDownloading, when); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := db.Media().Get(ctx, id)
	if got.Status != domain.MediaDownloading {
		t.Errorf("SetStatus: got %q", got.Status)
	}

	if err := db.Media().SetError(ctx, id, domain.MediaFailed, "boom", when); err != nil {
		t.Fatalf("SetError: %v", err)
	}
	got, _ = db.Media().Get(ctx, id)
	if got.Status != domain.MediaFailed {
		t.Errorf("SetError status: got %q", got.Status)
	}
	if got.LastError != "boom" {
		t.Errorf("SetError cause: got %q", got.LastError)
	}
	if got.Attempts != 1 {
		t.Errorf("SetError attempts: got %d, want 1", got.Attempts)
	}

	if err := db.Media().MarkDownloaded(ctx, id, "/x.mp4", 99, when); err != nil {
		t.Fatalf("MarkDownloaded: %v", err)
	}
	got, _ = db.Media().Get(ctx, id)
	if got.Status != domain.MediaDownloaded || got.FilePath != "/x.mp4" || got.FileSize != 99 {
		t.Errorf("MarkDownloaded: %+v", got)
	}
	if got.LastError != "" {
		t.Errorf("MarkDownloaded should clear error, got %q", got.LastError)
	}
}

func TestMediaListByStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	src := seedSource(t, db, profile.ID, true, nil)

	for _, ext := range []string{"p1", "p2", "p3"} {
		if _, err := db.Media().Upsert(ctx, newMedia(src.ID, ext, domain.MediaPending)); err != nil {
			t.Fatalf("Upsert %s: %v", ext, err)
		}
	}
	if _, err := db.Media().Upsert(ctx, newMedia(src.ID, "d1", domain.MediaDownloaded)); err != nil {
		t.Fatalf("Upsert d1: %v", err)
	}

	all, err := db.Media().ListByStatus(ctx, domain.MediaPending, 0)
	if err != nil {
		t.Fatalf("ListByStatus(no limit): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 pending, got %d", len(all))
	}

	limited, err := db.Media().ListByStatus(ctx, domain.MediaPending, 2)
	if err != nil {
		t.Fatalf("ListByStatus(limit 2): %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 with limit, got %d", len(limited))
	}
	// Ordered by id ascending.
	if limited[0].ID > limited[1].ID {
		t.Errorf("ListByStatus not ordered by id: %d then %d", limited[0].ID, limited[1].ID)
	}

	downloaded, err := db.Media().ListByStatus(ctx, domain.MediaDownloaded, 0)
	if err != nil {
		t.Fatalf("ListByStatus(downloaded): %v", err)
	}
	if len(downloaded) != 1 {
		t.Errorf("expected 1 downloaded, got %d", len(downloaded))
	}
}
