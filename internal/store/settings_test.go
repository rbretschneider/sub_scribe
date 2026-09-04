package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

func TestSessionSecretIsGeneratedOnceAndPersists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	first, err := db.Settings().SessionSecret(ctx)
	if err != nil {
		t.Fatalf("SessionSecret() error = %v", err)
	}
	if len(first) != sessionSecretBytes {
		t.Fatalf("secret length = %d, want %d", len(first), sessionSecretBytes)
	}

	second, err := db.Settings().SessionSecret(ctx)
	if err != nil {
		t.Fatalf("SessionSecret() second call error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("second call returned a different secret; sessions would not survive")
	}
}

func TestSourceFeedTokenRoundTrips(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profileID := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockOff, nil)

	now := time.Unix(1_700_000_000, 0)
	token := domain.NewFeedToken()
	id, err := db.Sources().Create(ctx, domain.Source{
		Name: "Channel", URL: "https://youtube.com/@c", CollectionType: domain.CollectionChannel,
		MediaProfileID: profileID, Enabled: true, FeedToken: token,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	source, err := db.Sources().Get(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if source.FeedToken != token {
		t.Errorf("FeedToken = %q, want %q", source.FeedToken, token)
	}
}

func TestSourceUpdateDoesNotRotateTheFeedToken(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profileID := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockOff, nil)

	now := time.Unix(1_700_000_000, 0)
	token := domain.NewFeedToken()
	id, err := db.Sources().Create(ctx, domain.Source{
		Name: "Channel", URL: "https://youtube.com/@c", CollectionType: domain.CollectionChannel,
		MediaProfileID: profileID, Enabled: true, FeedToken: token,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	source, err := db.Sources().Get(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	source.Name = "Renamed"
	// Even a caller that clears the field cannot rotate the stored token.
	source.FeedToken = ""
	if err := db.Sources().Update(ctx, source); err != nil {
		t.Fatalf("update source: %v", err)
	}

	after, err := db.Sources().Get(ctx, id)
	if err != nil {
		t.Fatalf("get source after update: %v", err)
	}
	if after.FeedToken != token {
		t.Errorf("FeedToken after update = %q, want the original %q", after.FeedToken, token)
	}
}

func TestFeedTokenBackfillFillsEveryEmptyToken(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	profileID := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockOff, nil)

	// Sources created before the migration existed have an empty token, which
	// the schema default reproduces when Create is given a zero FeedToken.
	now := time.Unix(1_700_000_000, 0)
	var ids []int64
	for range 3 {
		id, err := db.Sources().Create(ctx, domain.Source{
			Name: "Legacy", URL: "https://youtube.com/@l", CollectionType: domain.CollectionChannel,
			MediaProfileID: profileID, Enabled: true,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("create source: %v", err)
		}
		ids = append(ids, id)
	}

	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrationNamed(t, "add_source_feed_tokens").run(tx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	seen := make(map[string]bool)
	for _, id := range ids {
		source, err := db.Sources().Get(ctx, id)
		if err != nil {
			t.Fatalf("get source %d: %v", id, err)
		}
		if source.FeedToken == "" {
			t.Fatalf("source %d still has an empty feed token", id)
		}
		if seen[source.FeedToken] {
			t.Fatalf("token %q was assigned to more than one source", source.FeedToken)
		}
		seen[source.FeedToken] = true
	}
}
