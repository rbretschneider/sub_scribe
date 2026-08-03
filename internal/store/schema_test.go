package store

import (
	"context"
	"testing"

	"sub_scribe/internal/domain"
)

// migrationNamed returns the migration with the given name, so a test exercises
// the statement that actually ships rather than a copy of it that can drift.
func migrationNamed(t *testing.T, name string) migration {
	t.Helper()
	for _, m := range migrations {
		if m.name == name {
			return m
		}
	}
	t.Fatalf("no migration named %q", name)
	return migration{}
}

// replayMigration re-runs a migration against the open database. Migrations are
// forward-only and already applied by Open, so a test that needs to see one act
// on legacy-shaped data seeds that data first and replays the statement.
func replayMigration(t *testing.T, db *DB, name string) {
	t.Helper()
	if _, err := db.sql.Exec(migrationNamed(t, name).stmt); err != nil {
		t.Fatalf("replay migration %q: %v", name, err)
	}
}

// seedProfileWithSponsorBlock stores a profile with the given SponsorBlock
// settings and returns its id.
func seedProfileWithSponsorBlock(t *testing.T, db *DB, mode domain.SponsorBlockMode, categories []domain.SponsorBlockCategory) int64 {
	t.Helper()
	id, err := db.Profiles().Create(context.Background(), domain.MediaProfile{
		Name:                   "Seeded",
		OutputPathTemplate:     "{{ source_name }}/{{ title }}",
		Kind:                   domain.MediaVideo,
		MetadataFormat:         domain.MetadataEpisode,
		SponsorBlockMode:       mode,
		SponsorBlockCategories: categories,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return id
}

// categoriesOf reads back a profile's stored SponsorBlock categories.
func categoriesOf(t *testing.T, db *DB, id int64) []domain.SponsorBlockCategory {
	t.Helper()
	profile, err := db.Profiles().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	return profile.SponsorBlockCategories
}

// TestSponsorBlockMigrationWritesTheDefaultCategoryDown covers the profile
// seeded before this change: mode "remove" with no categories, which the builder
// used to expand into a hidden set and now expands into nothing at all. Without
// the migration such a profile would quietly stop removing sponsors.
func TestSponsorBlockMigrationWritesTheDefaultCategoryDown(t *testing.T) {
	db := openTestDB(t)
	id := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockRemove, nil)

	replayMigration(t, db, "sponsorblock_categories_are_explicit")

	got := categoriesOf(t, db, id)
	want := []domain.SponsorBlockCategory{domain.SponsorBlockSponsor}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("categories = %v, want %v", got, want)
	}
}

// TestSponsorBlockMigrationLeavesAChosenSetAlone: a user who picked their own
// categories has said what they want, and an upgrade must not overrule them.
func TestSponsorBlockMigrationLeavesAChosenSetAlone(t *testing.T) {
	db := openTestDB(t)
	chosen := []domain.SponsorBlockCategory{domain.SponsorBlockIntro, domain.SponsorBlockOutro}
	id := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockRemove, chosen)

	replayMigration(t, db, "sponsorblock_categories_are_explicit")

	got := categoriesOf(t, db, id)
	if len(got) != len(chosen) || got[0] != chosen[0] || got[1] != chosen[1] {
		t.Errorf("categories = %v, want the user's own %v", got, chosen)
	}
}

// TestSponsorBlockMigrationSkipsDisabledProfiles keeps a profile with
// SponsorBlock switched off from being handed categories it would use the moment
// someone turned it on.
func TestSponsorBlockMigrationSkipsDisabledProfiles(t *testing.T) {
	db := openTestDB(t)
	id := seedProfileWithSponsorBlock(t, db, domain.SponsorBlockOff, nil)

	replayMigration(t, db, "sponsorblock_categories_are_explicit")

	if got := categoriesOf(t, db, id); len(got) != 0 {
		t.Errorf("categories = %v, want none for a disabled profile", got)
	}
}
