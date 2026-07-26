package tasks

import (
	"context"
	"errors"
	"testing"

	"sub_scribe/internal/jobs"
)

// fakeIndexer records the source id it was asked to index and returns a canned error.
type fakeIndexer struct {
	called   bool
	sourceID int64
	err      error
}

func (f *fakeIndexer) IndexSource(_ context.Context, sourceID int64) error {
	f.called = true
	f.sourceID = sourceID
	return f.err
}

// fakeDownloader records the media id it was asked to download.
type fakeDownloader struct {
	called  bool
	mediaID int64
	err     error
}

func (f *fakeDownloader) DownloadMedia(_ context.Context, mediaID int64) error {
	f.called = true
	f.mediaID = mediaID
	return f.err
}

// fakeRetainer records the source id whose retention it was asked to enforce.
type fakeRetainer struct {
	called   bool
	sourceID int64
	err      error
}

func (f *fakeRetainer) EnforceRetention(_ context.Context, sourceID int64) error {
	f.called = true
	f.sourceID = sourceID
	return f.err
}

// fakeRedownloader records the source id whose redownload it was asked to enforce.
type fakeRedownloader struct {
	called   bool
	sourceID int64
	err      error
}

func (f *fakeRedownloader) EnforceRedownload(_ context.Context, sourceID int64) error {
	f.called = true
	f.sourceID = sourceID
	return f.err
}

func id(v int64) *int64 { return &v }

func TestRedownloadHandler(t *testing.T) {
	t.Run("delegates with source id", func(t *testing.T) {
		fake := &fakeRedownloader{}
		if err := RedownloadHandler(fake).Handle(context.Background(), jobs.Task{ID: 1, SourceID: id(12)}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fake.called || fake.sourceID != 12 {
			t.Errorf("called=%v sourceID=%d, want called for source 12", fake.called, fake.sourceID)
		}
	})
	t.Run("missing source id fails without calling service", func(t *testing.T) {
		fake := &fakeRedownloader{}
		if err := RedownloadHandler(fake).Handle(context.Background(), jobs.Task{ID: 2}); err == nil {
			t.Error("expected error for missing source id")
		}
		if fake.called {
			t.Error("service should not be called without a source id")
		}
	})
}

func TestIndexHandler(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name       string
		task       jobs.Task
		fakeErr    error
		wantCalled bool
		wantID     int64
		wantErr    bool
	}{
		{
			name:       "delegates with source id",
			task:       jobs.Task{ID: 1, SourceID: id(42)},
			wantCalled: true,
			wantID:     42,
		},
		{
			name:       "propagates service error",
			task:       jobs.Task{ID: 2, SourceID: id(7)},
			fakeErr:    errBoom,
			wantCalled: true,
			wantID:     7,
			wantErr:    true,
		},
		{
			name:       "missing source id fails without calling service",
			task:       jobs.Task{ID: 3, SourceID: nil},
			wantCalled: false,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeIndexer{err: tt.fakeErr}
			err := IndexHandler(fake).Handle(context.Background(), tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if fake.called != tt.wantCalled {
				t.Fatalf("called = %v, want %v", fake.called, tt.wantCalled)
			}
			if tt.wantCalled && fake.sourceID != tt.wantID {
				t.Fatalf("sourceID = %d, want %d", fake.sourceID, tt.wantID)
			}
			if tt.fakeErr != nil && !errors.Is(err, tt.fakeErr) {
				t.Fatalf("err = %v, want wrap of %v", err, tt.fakeErr)
			}
		})
	}
}

func TestDownloadHandler(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name       string
		task       jobs.Task
		fakeErr    error
		wantCalled bool
		wantID     int64
		wantErr    bool
	}{
		{
			name:       "delegates with media id",
			task:       jobs.Task{ID: 1, MediaID: id(99)},
			wantCalled: true,
			wantID:     99,
		},
		{
			name:       "propagates service error",
			task:       jobs.Task{ID: 2, MediaID: id(5)},
			fakeErr:    errBoom,
			wantCalled: true,
			wantID:     5,
			wantErr:    true,
		},
		{
			name:       "missing media id fails without calling service",
			task:       jobs.Task{ID: 3, MediaID: nil},
			wantCalled: false,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDownloader{err: tt.fakeErr}
			err := DownloadHandler(fake).Handle(context.Background(), tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if fake.called != tt.wantCalled {
				t.Fatalf("called = %v, want %v", fake.called, tt.wantCalled)
			}
			if tt.wantCalled && fake.mediaID != tt.wantID {
				t.Fatalf("mediaID = %d, want %d", fake.mediaID, tt.wantID)
			}
			if tt.fakeErr != nil && !errors.Is(err, tt.fakeErr) {
				t.Fatalf("err = %v, want wrap of %v", err, tt.fakeErr)
			}
		})
	}
}

func TestCleanupHandler(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name       string
		task       jobs.Task
		fakeErr    error
		wantCalled bool
		wantID     int64
		wantErr    bool
	}{
		{
			name:       "delegates with source id",
			task:       jobs.Task{ID: 1, SourceID: id(3)},
			wantCalled: true,
			wantID:     3,
		},
		{
			name:       "propagates service error",
			task:       jobs.Task{ID: 2, SourceID: id(8)},
			fakeErr:    errBoom,
			wantCalled: true,
			wantID:     8,
			wantErr:    true,
		},
		{
			name:       "missing source id fails without calling service",
			task:       jobs.Task{ID: 3, SourceID: nil},
			wantCalled: false,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRetainer{err: tt.fakeErr}
			err := CleanupHandler(fake).Handle(context.Background(), tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if fake.called != tt.wantCalled {
				t.Fatalf("called = %v, want %v", fake.called, tt.wantCalled)
			}
			if tt.wantCalled && fake.sourceID != tt.wantID {
				t.Fatalf("sourceID = %d, want %d", fake.sourceID, tt.wantID)
			}
			if tt.fakeErr != nil && !errors.Is(err, tt.fakeErr) {
				t.Fatalf("err = %v, want wrap of %v", err, tt.fakeErr)
			}
		})
	}
}
