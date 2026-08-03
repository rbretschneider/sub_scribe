package ytdlp

import (
	"context"

	"sub_scribe/internal/domain"
)

// FakeRunner is a test double for Runner. Each method delegates to its
// corresponding func field, or returns a zero value when that field is nil, so
// other packages can drive Runner behavior in their tests.
type FakeRunner struct {
	IndexFunc    func(ctx context.Context, url string, opts IndexOptions) ([]IndexEntry, error)
	MetadataFunc func(ctx context.Context, url, cookiesPath string) (IndexEntry, error)
	DownloadFunc func(ctx context.Context, url string, opts DownloadOptions, onProgress ProgressFunc) (DownloadResult, error)
	ArtworkFunc  func(ctx context.Context, url, cookiesPath string) (domain.ChannelArtwork, error)
}

var _ Runner = (*FakeRunner)(nil)

// Index delegates to IndexFunc, returning no entries when it is nil.
func (f *FakeRunner) Index(ctx context.Context, url string, opts IndexOptions) ([]IndexEntry, error) {
	if f.IndexFunc == nil {
		return nil, nil
	}
	return f.IndexFunc(ctx, url, opts)
}

// Metadata delegates to MetadataFunc, returning a zero entry when it is nil.
func (f *FakeRunner) Metadata(ctx context.Context, url, cookiesPath string) (IndexEntry, error) {
	if f.MetadataFunc == nil {
		return IndexEntry{}, nil
	}
	return f.MetadataFunc(ctx, url, cookiesPath)
}

// Download delegates to DownloadFunc, returning a zero result when it is nil.
func (f *FakeRunner) Download(ctx context.Context, url string, opts DownloadOptions, onProgress ProgressFunc) (DownloadResult, error) {
	if f.DownloadFunc == nil {
		return DownloadResult{}, nil
	}
	return f.DownloadFunc(ctx, url, opts, onProgress)
}

// Artwork delegates to ArtworkFunc, returning no artwork when it is nil.
func (f *FakeRunner) Artwork(ctx context.Context, url, cookiesPath string) (domain.ChannelArtwork, error) {
	if f.ArtworkFunc == nil {
		return domain.ChannelArtwork{}, nil
	}
	return f.ArtworkFunc(ctx, url, cookiesPath)
}
