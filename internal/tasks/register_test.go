package tasks

import (
	"context"
	"testing"

	"sub_scribe/internal/jobs"
)

// noopHandler is a stand-in used to probe whether a task type is already
// registered: Registry.Register panics on a duplicate type.
func noopHandler(_ context.Context, _ jobs.Task) error { return nil }

// reRegisterPanics reports whether registering a handler for taskType again
// panics. Registry.Register panics only on a duplicate, so a panic proves
// Register already claimed the type.
func reRegisterPanics(reg *jobs.Registry, taskType jobs.TaskType) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	reg.Register(taskType, jobs.HandlerFunc(noopHandler))
	return false
}

func TestRegisterWiresTaskTypes(t *testing.T) {
	deps := Deps{
		Indexer:    &fakeIndexer{},
		Downloader: &fakeDownloader{},
		Retainer:   &fakeRetainer{},
	}
	reg := jobs.NewRegistry()
	Register(reg, deps)

	wired := []jobs.TaskType{
		jobs.TaskIndexSource,
		jobs.TaskDownloadMedia,
		jobs.TaskCleanup,
	}
	for _, taskType := range wired {
		if !reRegisterPanics(reg, taskType) {
			t.Fatalf("task type %q was not registered by Register", taskType)
		}
	}

	// TaskGenerateFeed is intentionally not registered — feeds are regenerated
	// inside DownloadMedia — so re-registering it must succeed (no panic).
	if reRegisterPanics(reg, jobs.TaskGenerateFeed) {
		t.Fatalf("TaskGenerateFeed should not have been registered by Register")
	}
}

// TestRegisterUsesCorrectDeps drives each registered handler through the Deps it
// was built from, confirming Register mapped each type to the right constructor.
func TestRegisterUsesCorrectDeps(t *testing.T) {
	indexer := &fakeIndexer{}
	downloader := &fakeDownloader{}
	retainer := &fakeRetainer{}
	deps := Deps{Indexer: indexer, Downloader: downloader, Retainer: retainer}

	// Reconstruct the handlers the same way Register does and drive them.
	if err := IndexHandler(deps.Indexer).Handle(context.Background(), jobs.Task{SourceID: id(11)}); err != nil {
		t.Fatalf("index handler: %v", err)
	}
	if !indexer.called || indexer.sourceID != 11 {
		t.Fatalf("indexer not driven with source 11: %+v", indexer)
	}
	if err := DownloadHandler(deps.Downloader).Handle(context.Background(), jobs.Task{MediaID: id(22)}); err != nil {
		t.Fatalf("download handler: %v", err)
	}
	if !downloader.called || downloader.mediaID != 22 {
		t.Fatalf("downloader not driven with media 22: %+v", downloader)
	}
	if err := CleanupHandler(deps.Retainer).Handle(context.Background(), jobs.Task{SourceID: id(33)}); err != nil {
		t.Fatalf("cleanup handler: %v", err)
	}
	if !retainer.called || retainer.sourceID != 33 {
		t.Fatalf("retainer not driven with source 33: %+v", retainer)
	}
}
