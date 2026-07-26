package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// Scale of the seeded archive: what "a few channels on a 365-day window" looks
// like once a year of a prolific channel is indexed.
const (
	benchSources        = 6
	benchMediaPerSource = 3000
)

// seedLargeArchive builds a database holding a realistic large archive and
// returns it. It is deliberately built through the repositories, so the cost of
// the write path is exercised too.
func seedLargeArchive(tb testing.TB) *DB {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "scale.db")
	db, err := Open(path)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { db.Close() })

	ctx := context.Background()
	profileID, err := db.Profiles().Create(ctx, domain.MediaProfile{
		Name: "Bench", OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind: domain.MediaVideo, MetadataFormat: domain.MetadataMovie,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		tb.Fatalf("create profile: %v", err)
	}

	statuses := []domain.MediaStatus{
		domain.MediaSkipped, domain.MediaSkipped, domain.MediaSkipped,
		domain.MediaDownloaded, domain.MediaFailed, domain.MediaPending,
	}

	media, tasks := db.Media(), db.Tasks()
	for s := 0; s < benchSources; s++ {
		sourceID, err := db.Sources().Create(ctx, domain.Source{
			Name: fmt.Sprintf("Channel %d", s), URL: fmt.Sprintf("https://youtube.com/@c%d", s),
			CollectionType: domain.CollectionChannel, MediaProfileID: profileID,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			tb.Fatalf("create source: %v", err)
		}
		for i := 0; i < benchMediaPerSource; i++ {
			status := statuses[i%len(statuses)]
			mediaID, err := media.Upsert(ctx, domain.Media{
				SourceID: sourceID, ExternalID: fmt.Sprintf("vid%d-%d", s, i),
				Status: status, FileSize: int64(i) * 1000,
				Metadata: domain.MediaMetadata{
					Title:      fmt.Sprintf("Channel %d Episode %d", s, i),
					UploadDate: now.AddDate(0, 0, -i%365),
				},
				CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				tb.Fatalf("upsert media: %v", err)
			}
			// One finished download task per item, as a real archive accumulates.
			task := jobs.NewTask(jobs.TaskDownloadMedia, now).ForMedia(mediaID)
			task.Status = jobs.StatusSucceeded
			if _, err := tasks.Enqueue(ctx, task); err != nil {
				tb.Fatalf("enqueue: %v", err)
			}
		}
	}
	return db
}

// timed runs fn and reports how long it took, failing if it exceeds budget —
// these are the queries behind a page load, so they have to stay interactive.
func timed(tb testing.TB, label string, budget time.Duration, fn func() error) {
	tb.Helper()
	start := time.Now()
	if err := fn(); err != nil {
		tb.Fatalf("%s: %v", label, err)
	}
	elapsed := time.Since(start)
	tb.Logf("%-34s %8.1fms", label, float64(elapsed.Microseconds())/1000)
	if elapsed > budget {
		tb.Errorf("%s took %v, over the %v budget for an interactive page", label, elapsed, budget)
	}
}

// TestScaleOfReadPaths measures every query behind a page load against a large
// archive. The budgets are what keeps the UI feeling immediate as the archive
// grows; a regression here is a slow page, not a broken one, so it is worth
// catching in CI rather than in the browser.
func TestScaleOfReadPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("seeding a large archive is slow")
	}
	db := seedLargeArchive(t)
	ctx := context.Background()
	media, tasks := db.Media(), db.Tasks()
	total := benchSources * benchMediaPerSource
	t.Logf("archive: %d media rows, %d tasks", total, total)

	const pageBudget = 150 * time.Millisecond

	timed(t, "dashboard: counts by status", pageBudget, func() error {
		_, err := media.CountsByStatus(ctx)
		return err
	})
	timed(t, "dashboard: total bytes", pageBudget, func() error {
		_, err := media.TotalDownloadedBytes(ctx)
		return err
	})
	timed(t, "dashboard: source stats", pageBudget, func() error {
		_, err := media.StatsBySource(ctx)
		return err
	})
	timed(t, "library: first page (120)", pageBudget, func() error {
		_, err := media.ListWithSource(ctx, "", 120)
		return err
	})
	timed(t, "library: filtered page (120)", pageBudget, func() error {
		_, err := media.ListWithSource(ctx, domain.MediaDownloaded, 120)
		return err
	})
	timed(t, "jobs: first page (200)", pageBudget, func() error {
		_, err := tasks.ListJobs(ctx, library.JobFilter{Limit: 200})
		return err
	})
	timed(t, "jobs: counts by status", pageBudget, func() error {
		_, err := tasks.CountsByStatus(ctx)
		return err
	})
	timed(t, "queue: claim next task", pageBudget, func() error {
		_, err := tasks.Claim(ctx, now)
		return err
	})
	timed(t, "startup: active media ids", pageBudget, func() error {
		_, err := tasks.ActiveMediaIDs(ctx)
		return err
	})
	timed(t, "startup: delete orphan tasks", 2*time.Second, func() error {
		_, err := tasks.DeleteOrphans(ctx)
		return err
	})
	timed(t, "index: exists check", pageBudget, func() error {
		_, err := media.ExistsBySource(ctx, 1, "vid0-2999")
		return err
	})
	timed(t, "startup: list pending media", time.Second, func() error {
		_, err := media.ListByStatus(ctx, domain.MediaPending, 0)
		return err
	})
}
