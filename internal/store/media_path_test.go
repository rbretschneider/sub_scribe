package store

import (
	"context"
	"testing"

	"sub_scribe/internal/domain"
)

// seedSourceForMedia inserts a profile and a source so media rows have the
// parents their foreign keys require.
func seedSourceForMedia(t *testing.T, db *DB) int64 {
	t.Helper()
	profile := seedProfile(t, db)
	return seedSource(t, db, profile.ID, true, nil).ID
}

// TestUpsertDoesNotDisturbDownloadState pins the behaviour that makes re-indexing
// safe: a scan re-reports every item it finds, and if that overwrote file_path
// and status then every scan would forget what had already been downloaded.
//
// It is pinned here because the rule is invisible at the call site — Upsert takes
// a whole media value, so passing one with a new path looks like it would save
// the new path — and a fake that did save it made a genuine bug pass its test.
func TestUpsertDoesNotDisturbDownloadState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	sourceID := seedSourceForMedia(t, db)
	repo := db.Media()

	id, err := repo.Upsert(ctx, domain.Media{
		SourceID: sourceID, ExternalID: "abc", Status: domain.MediaPending,
		Metadata:  domain.MediaMetadata{Title: "Original"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := repo.MarkDownloaded(ctx, id, "/media/Chan/original.mkv", 1234, now); err != nil {
		t.Fatalf("MarkDownloaded: %v", err)
	}

	// A later scan re-reports the item, carrying no download state of its own.
	if _, err := repo.Upsert(ctx, domain.Media{
		SourceID: sourceID, ExternalID: "abc", Status: domain.MediaPending,
		Metadata:  domain.MediaMetadata{Title: "Updated Title"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert (re-index): %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Metadata.Title != "Updated Title" {
		t.Errorf("title = %q, want the re-indexed title", got.Metadata.Title)
	}
	if got.FilePath != "/media/Chan/original.mkv" {
		t.Errorf("file path = %q, want the download to be remembered", got.FilePath)
	}
	if got.Status != domain.MediaDownloaded {
		t.Errorf("status = %q, want %q", got.Status, domain.MediaDownloaded)
	}
	if got.FileSize != 1234 {
		t.Errorf("file size = %d, want 1234", got.FileSize)
	}
}

// TestSetFilePathRecordsAMoveWithoutTouchingState covers the operation renaming
// needs. Because Upsert ignores file_path, a rename that went through Upsert
// moved the file on disk and told the database nothing — leaving every later
// write, including metadata sidecars, aimed at a path that no longer exists.
func TestSetFilePathRecordsAMoveWithoutTouchingState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	sourceID := seedSourceForMedia(t, db)
	repo := db.Media()

	id, err := repo.Upsert(ctx, domain.Media{
		SourceID: sourceID, ExternalID: "abc", Status: domain.MediaPending,
		Metadata:  domain.MediaMetadata{Title: "Video"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := repo.MarkDownloaded(ctx, id, "/media/Chan/old name.mkv", 99, now); err != nil {
		t.Fatalf("MarkDownloaded: %v", err)
	}

	const moved = "/media/Chan/Season 2026/s2026e010101 - Video.mkv"
	if err := repo.SetFilePath(ctx, id, moved, now); err != nil {
		t.Fatalf("SetFilePath: %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FilePath != moved {
		t.Errorf("file path = %q, want %q", got.FilePath, moved)
	}
	if got.Status != domain.MediaDownloaded || got.FileSize != 99 {
		t.Errorf("moving a file disturbed its state: status=%q size=%d", got.Status, got.FileSize)
	}
}
