package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

// TestReconcileScalesWithArchiveSize measures startup recovery against an
// archive the size a few channels on a year-long window produce.
//
// Recovery runs before any work is claimed, so its cost is dead time on every
// boot. It also touches the filesystem once per item, which is exactly the shape
// that turns quadratic without care — and the media volume is often a slow
// network or bind mount, where each extra directory read is expensive.
func TestReconcileScalesWithArchiveSize(t *testing.T) {
	if testing.Short() {
		t.Skip("seeding a large archive is slow")
	}

	const items = 6000
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)

	// Everything skipped and sharing one directory: the worst case for a
	// per-item directory scan, and what a wide date window actually produces.
	dir := filepath.Join(h.mediaDir, "My Channel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < items; i++ {
		mustUpsertMedia(t, h, domain.Media{
			SourceID: sourceID, ExternalID: fmt.Sprintf("vid%d", i),
			Status:   domain.MediaSkipped,
			Metadata: domain.MediaMetadata{Title: fmt.Sprintf("Episode %d", i)},
		})
	}
	// A tenth of them are actually on disk, as after a partial archive run.
	for i := 0; i < items/10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("Episode %d.mkv", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	start := time.Now()
	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("reconcile over %d items (%d on disk): %v", items, items/10, elapsed)
	if report.ExistingFilesAdopted != items/10 {
		t.Errorf("adopted %d, want %d", report.ExistingFilesAdopted, items/10)
	}
	// Startup must stay snappy: this is time before the first download can begin.
	if elapsed > 3*time.Second {
		t.Errorf("startup recovery took %v for %d items — too slow to run on every boot",
			elapsed, items)
	}
}
