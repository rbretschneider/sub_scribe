package store

import (
	"context"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

func TestMediaReadViews(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	source := seedSource(t, db, profile.ID, true, nil)
	media := db.Media()
	now := time.Unix(1_700_000_000, 0).UTC()

	insert := func(ext string, status domain.MediaStatus, size int64) {
		if _, err := media.Upsert(ctx, domain.Media{
			SourceID: source.ID, ExternalID: ext, Status: status, FileSize: size,
			Metadata:  domain.MediaMetadata{Title: "T-" + ext},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", ext, err)
		}
	}
	insert("a", domain.MediaDownloaded, 1000)
	insert("b", domain.MediaDownloaded, 2000)
	insert("c", domain.MediaPending, 0)
	insert("d", domain.MediaDownloading, 0)

	counts, err := media.CountsByStatus(ctx)
	if err != nil {
		t.Fatalf("CountsByStatus: %v", err)
	}
	if counts[domain.MediaDownloaded] != 2 || counts[domain.MediaPending] != 1 || counts[domain.MediaDownloading] != 1 {
		t.Errorf("counts = %v, want downloaded 2 / pending 1 / downloading 1", counts)
	}

	total, err := media.TotalDownloadedBytes(ctx)
	if err != nil {
		t.Fatalf("TotalDownloadedBytes: %v", err)
	}
	if total != 3000 {
		t.Errorf("TotalDownloadedBytes = %d, want 3000", total)
	}

	all, err := media.ListWithSource(ctx, library.MediaQuery{})
	if err != nil {
		t.Fatalf("ListWithSource(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListWithSource(all) len = %d, want 4", len(all))
	}
	if all[0].SourceName != source.Name {
		t.Errorf("SourceName = %q, want %q", all[0].SourceName, source.Name)
	}

	downloaded, err := media.ListWithSource(ctx, library.MediaQuery{Status: domain.MediaDownloaded})
	if err != nil {
		t.Fatalf("ListWithSource(downloaded): %v", err)
	}
	if len(downloaded) != 2 {
		t.Errorf("downloaded filter len = %d, want 2", len(downloaded))
	}

	limited, err := media.ListWithSource(ctx, library.MediaQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ListWithSource(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limited len = %d, want 1", len(limited))
	}
}

func TestListWithSourceSearchesTitlesAndScopesToASource(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	source := seedSource(t, db, profile.ID, true, nil)
	other := seedSource(t, db, profile.ID, true, nil)
	media := db.Media()
	now := time.Unix(1_700_000_000, 0).UTC()

	insert := func(sourceID int64, ext, title string) {
		if _, err := media.Upsert(ctx, domain.Media{
			SourceID: sourceID, ExternalID: ext, Status: domain.MediaDownloaded,
			Metadata:  domain.MediaMetadata{Title: title},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", ext, err)
		}
	}
	insert(source.ID, "a", "GPS Hidden Messages")
	insert(source.ID, "b", "The 100% Solution")
	insert(other.ID, "c", "GPS on the Other Channel")

	bySource, err := media.ListWithSource(ctx, library.MediaQuery{SourceID: source.ID})
	if err != nil {
		t.Fatalf("ListWithSource(source): %v", err)
	}
	if len(bySource) != 2 {
		t.Errorf("source filter len = %d, want 2", len(bySource))
	}

	// Case-insensitive, and scoped by the source filter at the same time.
	found, err := media.ListWithSource(ctx, library.MediaQuery{SourceID: source.ID, Search: "gps"})
	if err != nil {
		t.Fatalf("ListWithSource(search): %v", err)
	}
	if len(found) != 1 || found[0].Media.ExternalID != "a" {
		t.Errorf("search hit = %+v, want the one GPS video in this source", found)
	}

	// LIKE wildcards in the typed text must match literally, not everything.
	literal, err := media.ListWithSource(ctx, library.MediaQuery{Search: "100%"})
	if err != nil {
		t.Fatalf("ListWithSource(literal): %v", err)
	}
	if len(literal) != 1 || literal[0].Media.ExternalID != "b" {
		t.Errorf("literal %% search = %+v, want only The 100%% Solution", literal)
	}
}
