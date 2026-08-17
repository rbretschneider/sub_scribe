package store

import (
	"context"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

// TestListSlatedForDeletion exercises the real query. The service-level tests
// use a fake repo, so a broken SQL scan here passes every test above it and
// only surfaces as a 500 in the browser.
func TestListSlatedForDeletion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profile := seedProfile(t, db)
	// seedSource already sets RetentionAfter to 30 days, which is what makes
	// these rows eligible for the retention queue.
	source := seedSource(t, db, profile.ID, true, nil)
	media := db.Media()
	now := time.Unix(1_700_000_000, 0).UTC()

	add := func(ext string, downloadedAt time.Time) {
		if _, err := media.Upsert(ctx, domain.Media{
			SourceID: source.ID, ExternalID: ext, Status: domain.MediaDownloaded,
			Metadata:     domain.MediaMetadata{Title: "T-" + ext},
			DownloadedAt: &downloadedAt,
			CreatedAt:    now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", ext, err)
		}
	}
	add("old", now.Add(-24*time.Hour))
	add("new", now)

	items, err := media.ListSlatedForDeletion(ctx, 30)
	if err != nil {
		t.Fatalf("ListSlatedForDeletion: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Soonest to expire first.
	if items[0].Media.ExternalID != "old" {
		t.Errorf("first item = %q, want \"old\" (soonest to expire)", items[0].Media.ExternalID)
	}
	if items[0].Expiration.IsZero() {
		t.Error("expiration was not populated")
	}
	if !items[0].Expiration.Before(items[1].Expiration) {
		t.Errorf("expirations not ordered: %v then %v", items[0].Expiration, items[1].Expiration)
	}
}
