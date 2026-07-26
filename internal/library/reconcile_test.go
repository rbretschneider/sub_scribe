package library

import (
	"context"
	"os"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/ytdlp"
)

func TestReconcileRequeuesPendingMediaThatLostItsTask(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)

	// Two pending items; only one still has a live queue entry. The other is the
	// case that left the app doing nothing: the media row survived, its task did not.
	stranded := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "stranded", Status: domain.MediaPending})
	queued := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "queued", Status: domain.MediaPending})
	h.queue.activeMedia[queued] = true

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.StrandedMediaQueued != 1 {
		t.Errorf("StrandedMediaQueued = %d, want 1", report.StrandedMediaQueued)
	}
	downloads := h.tasks.tasksOfType(jobs.TaskDownloadMedia)
	if len(downloads) != 1 {
		t.Fatalf("enqueued %d download tasks, want 1", len(downloads))
	}
	if downloads[0].MediaID == nil || *downloads[0].MediaID != stranded {
		t.Errorf("requeued media %v, want %d", downloads[0].MediaID, stranded)
	}
	if downloads[0].SourceID == nil || *downloads[0].SourceID != sourceID {
		t.Errorf("requeued task lost its source association: %v", downloads[0].SourceID)
	}
}

func TestReconcileResetsInterruptedDownloads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	interrupted := mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "mid-flight", Status: domain.MediaDownloading})

	report, err := h.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.InterruptedDownloads != 1 {
		t.Errorf("InterruptedDownloads = %d, want 1", report.InterruptedDownloads)
	}
	media, err := h.media.Get(ctx, interrupted)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaPending {
		t.Errorf("status = %q, want %q — an interrupted item must become retryable",
			media.Status, domain.MediaPending)
	}
	// Reset and requeued in the same pass, so the item actually gets picked up.
	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 1 {
		t.Errorf("enqueued %d download tasks, want 1", got)
	}
}

func TestReconcileReportsQueueRepairs(t *testing.T) {
	h := newHarness(t)
	h.queue.orphansRemoved = 496
	h.queue.runningRequeued = 2

	report, err := h.svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.OrphanTasksRemoved != 496 {
		t.Errorf("OrphanTasksRemoved = %d, want 496", report.OrphanTasksRemoved)
	}
	if report.RunningTasksRequeued != 2 {
		t.Errorf("RunningTasksRequeued = %d, want 2", report.RunningTasksRequeued)
	}
	if report.IsEmpty() {
		t.Error("IsEmpty() = true, want false when repairs were made")
	}
}

func TestReconcileOnHealthyQueueChangesNothing(t *testing.T) {
	h := newHarness(t)
	sourceID := h.seedSource(t)
	mustUpsertMedia(t, h, domain.Media{
		SourceID: sourceID, ExternalID: "done", Status: domain.MediaDownloaded})

	report, err := h.svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !report.IsEmpty() {
		t.Errorf("report = %+v, want empty", report)
	}
	if got := len(h.tasks.tasksOfType(jobs.TaskDownloadMedia)); got != 0 {
		t.Errorf("enqueued %d download tasks, want none", got)
	}
}

func TestDownloadFetchesTheUploadDateWhenIndexingLeftItBlank(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID:   sourceID,
		ExternalID: "vid1",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "A Video"},
	})
	uploaded := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	h.runner.metadata = ytdlp.IndexEntry{ExternalID: "vid1", Title: "A Video", UploadDate: uploaded}
	h.runner.result = ytdlp.DownloadResult{FilePath: h.mediaDir + "/out.mkv", FileSize: 10}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.metadataCalls != 1 {
		t.Errorf("metadata lookups = %d, want 1", h.runner.metadataCalls)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if !media.Metadata.UploadDate.Equal(uploaded) {
		t.Errorf("UploadDate = %v, want %v — the real date must be persisted before naming",
			media.Metadata.UploadDate, uploaded)
	}
}

func TestDownloadSkipsTheMetadataLookupWhenTheDateIsKnown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	known := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID:   sourceID,
		ExternalID: "vid2",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Known", UploadDate: known},
	})
	h.runner.result = ytdlp.DownloadResult{FilePath: h.mediaDir + "/out.mkv", FileSize: 10}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.metadataCalls != 0 {
		t.Errorf("metadata lookups = %d, want 0 — no extra request when the date is known",
			h.runner.metadataCalls)
	}
}

func TestUnavailableMediaIsSettledNotRetried(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID:   sourceID,
		ExternalID: "members",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Members only", UploadDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
	})
	h.runner.downloadErr = ytdlp.ErrUnavailable

	// No error: the task must not spend its retry budget on a settled answer.
	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia returned %v, want nil so the task is not retried", err)
	}

	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaUnavailable {
		t.Errorf("status = %q, want %q", media.Status, domain.MediaUnavailable)
	}
	if media.LastError == "" {
		t.Error("LastError is empty; the user needs to see why it was given up on")
	}
}

func TestUnavailableIsSettledWhereverItIsDiscovered(t *testing.T) {
	// A members-only video is refused at the metadata lookup, before the download
	// is ever attempted. That path must reach the same verdict as the download
	// itself, or the item burns its retry budget and reports a generic failure.
	tests := []struct {
		name  string
		apply func(runner *fakeRunner)
	}{
		{
			name:  "refused while fetching metadata",
			apply: func(runner *fakeRunner) { runner.metadataErr = ytdlp.ErrUnavailable },
		},
		{
			name:  "refused while downloading",
			apply: func(runner *fakeRunner) { runner.downloadErr = ytdlp.ErrUnavailable },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			sourceID := h.seedSource(t)
			// No upload date, so the metadata lookup runs.
			mediaID := mustUpsertMedia(t, h, domain.Media{
				SourceID: sourceID, ExternalID: "members", Status: domain.MediaPending,
				Metadata: domain.MediaMetadata{Title: "Members only"},
			})
			test.apply(h.runner)

			if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
				t.Fatalf("DownloadMedia returned %v, want nil so the task is not retried", err)
			}

			media, err := h.media.Get(ctx, mediaID)
			if err != nil {
				t.Fatalf("get media: %v", err)
			}
			if media.Status != domain.MediaUnavailable {
				t.Errorf("status = %q, want %q", media.Status, domain.MediaUnavailable)
			}
			if media.LastError == "" {
				t.Error("LastError is empty; the user needs to see why it was given up on")
			}
		})
	}
}

func TestDownloadKeepsScratchFilesOffTheMediaVolume(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID:   sourceID,
		ExternalID: "vid3",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "T", UploadDate: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
	})
	h.runner.result = ytdlp.DownloadResult{FilePath: h.mediaDir + "/out.mkv", FileSize: 1}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.lastOpts.TempDir != h.tempDir {
		t.Errorf("TempDir = %q, want %q", h.runner.lastOpts.TempDir, h.tempDir)
	}
}

func TestPruneJobsRemovesHistoryPastTheRetentionWindow(t *testing.T) {
	h := newHarness(t)
	h.retention = 7 * 24 * time.Hour
	h.rebuildService()
	h.queue.finishedPruned = 12

	removed, err := h.svc.PruneJobs(context.Background())
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}

	if removed != 12 {
		t.Errorf("removed = %d, want 12", removed)
	}
	wantCutoff := h.now.Add(-7 * 24 * time.Hour)
	if !h.queue.pruneCutoff.Equal(wantCutoff) {
		t.Errorf("cutoff = %v, want %v", h.queue.pruneCutoff, wantCutoff)
	}
}

func TestPruneJobsKeepsEverythingWhenRetentionIsDisabled(t *testing.T) {
	h := newHarness(t) // retention defaults to zero
	h.queue.finishedPruned = 12

	removed, err := h.svc.PruneJobs(context.Background())
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}

	if removed != 0 {
		t.Errorf("removed = %d, want 0 — zero retention means keep forever", removed)
	}
	if !h.queue.pruneCutoff.IsZero() {
		t.Error("the queue should not have been asked to delete anything")
	}
}

func TestSetSourceEnabledPausesAndResumesWithoutTouchingSettings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	before, err := h.sources.Get(ctx, sourceID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}

	if err := h.svc.SetSourceEnabled(ctx, sourceID, false); err != nil {
		t.Fatalf("SetSourceEnabled(false): %v", err)
	}

	paused, err := h.sources.Get(ctx, sourceID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if paused.Enabled {
		t.Error("source is still enabled after being paused")
	}
	// Pausing must be safe: it is the alternative to deleting, so nothing else
	// about the source may change.
	if paused.URL != before.URL || paused.IndexFrequency != before.IndexFrequency ||
		paused.MediaProfileID != before.MediaProfileID || paused.DownloadCutoff != before.DownloadCutoff {
		t.Errorf("pausing altered the source's settings:\n before %+v\n after  %+v", before, paused)
	}

	if err := h.svc.SetSourceEnabled(ctx, sourceID, true); err != nil {
		t.Fatalf("SetSourceEnabled(true): %v", err)
	}
	resumed, err := h.sources.Get(ctx, sourceID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if !resumed.Enabled {
		t.Error("source is not enabled after being resumed")
	}
}

func TestSkippedVideosLeaveNoEmptyFoldersBehind(t *testing.T) {
	// Most items a large channel offers fall outside the date window and are never
	// downloaded. Creating the destination up front left an empty "Season <year>"
	// folder for every year the channel had existed.
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t)
	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID:   sourceID,
		ExternalID: "old",
		Status:     domain.MediaPending,
		Metadata: domain.MediaMetadata{
			Title:      "An Old Video",
			UploadDate: time.Date(2019, 4, 1, 0, 0, 0, 0, time.UTC),
		},
	})
	h.runner.downloadErr = ytdlp.ErrFilteredOut

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaSkipped {
		t.Fatalf("status = %q, want %q", media.Status, domain.MediaSkipped)
	}

	entries, err := os.ReadDir(h.mediaDir)
	if err != nil {
		t.Fatalf("read media dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("a skipped video created %v in the media directory; it should stay empty", names)
	}
}

func TestIndexPushesTheDateWindowDownToTheScan(t *testing.T) {
	// The window has to reach the provider, or the scan walks the whole back
	// catalogue and every out-of-window item costs a row, a queue entry, and a
	// metadata lookup before being discarded.
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	input := validInput(profileID)
	input.CutoffWindow = 30 * 24 * time.Hour
	source, err := h.svc.AddSource(ctx, input)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	if err := h.svc.IndexSource(ctx, source.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	want := h.now.AddDate(0, 0, -30).Format("20060102")
	if got := h.runner.lastIndexOpts.DateAfter; got != want {
		t.Errorf("scan DateAfter = %q, want %q", got, want)
	}
}

func TestIndexScansEverythingWhenThereIsNoWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sourceID := h.seedSource(t) // validInput sets no cutoff

	if err := h.svc.IndexSource(ctx, sourceID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	if got := h.runner.lastIndexOpts.DateAfter; got != "" {
		t.Errorf("scan DateAfter = %q, want empty so the whole catalogue is seen", got)
	}
}

func TestOutOfWindowVideosAreSkippedWithoutDownloading(t *testing.T) {
	// A first scan of a large channel queues its whole back catalogue, because
	// indexing is shallow and yields no upload dates. Once the real date is known
	// the item must be discarded here rather than by launching yt-dlp to refuse it.
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	input := validInput(profileID)
	input.CutoffWindow = 30 * 24 * time.Hour
	source, err := h.svc.AddSource(ctx, input)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	h.tasks.tasks = nil

	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: source.ID, ExternalID: "ancient", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Welcome to Computerphile!"},
	})
	// The lookup reveals it is years old — well outside the 30-day window.
	h.runner.metadata = ytdlp.IndexEntry{
		ExternalID: "ancient",
		Title:      "Welcome to Computerphile!",
		UploadDate: h.now.AddDate(-12, 0, 0),
	}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.downloadCalls != 0 {
		t.Errorf("download calls = %d, want 0 — the item is outside the window", h.runner.downloadCalls)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaSkipped {
		t.Errorf("status = %q, want %q", media.Status, domain.MediaSkipped)
	}
}

func TestInWindowVideosStillDownload(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	input := validInput(profileID)
	input.CutoffWindow = 30 * 24 * time.Hour
	source, err := h.svc.AddSource(ctx, input)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	mediaID := mustUpsertMedia(t, h, domain.Media{
		SourceID: source.ID, ExternalID: "fresh", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Recent", UploadDate: h.now.AddDate(0, 0, -3)},
	})
	h.runner.result = ytdlp.DownloadResult{FilePath: h.mediaDir + "/out.mkv", FileSize: 5}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.downloadCalls != 1 {
		t.Fatalf("download calls = %d, want 1", h.runner.downloadCalls)
	}
	media, err := h.media.Get(ctx, mediaID)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	if media.Status != domain.MediaDownloaded {
		t.Errorf("status = %q, want %q", media.Status, domain.MediaDownloaded)
	}
}

// mustUpsertMedia stores a media item and returns its id.
func mustUpsertMedia(t *testing.T, h *harness, media domain.Media) int64 {
	t.Helper()
	id, err := h.media.Upsert(context.Background(), media)
	if err != nil {
		t.Fatalf("upsert media: %v", err)
	}
	return id
}

// seedSource inserts a profile and a source, returning the source id.
func (h *harness) seedSource(t *testing.T) int64 {
	t.Helper()
	profileID := h.seedProfile(t)
	source, err := h.svc.AddSource(context.Background(), validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// AddSource queues an index task; clear it so download assertions are unambiguous.
	h.tasks.tasks = nil
	return source.ID
}
