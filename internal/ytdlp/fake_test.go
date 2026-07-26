package ytdlp

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerDelegates(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &FakeRunner{
		IndexFunc: func(_ context.Context, url string, opts IndexOptions) ([]IndexEntry, error) {
			if url != "u" || opts.CookiesPath != "c" {
				t.Errorf("IndexFunc got url=%q cookies=%q", url, opts.CookiesPath)
			}
			return []IndexEntry{{ExternalID: "x"}}, wantErr
		},
		DownloadFunc: func(_ context.Context, url string, opts DownloadOptions, _ ProgressFunc) (DownloadResult, error) {
			if url != "u" || opts.Format != "best" {
				t.Errorf("DownloadFunc got url=%q format=%q", url, opts.Format)
			}
			return DownloadResult{FilePath: "/f", FileSize: 7}, nil
		},
	}

	entries, err := fake.Index(context.Background(), "u", IndexOptions{CookiesPath: "c"})
	if !errors.Is(err, wantErr) || len(entries) != 1 || entries[0].ExternalID != "x" {
		t.Errorf("Index() = %v, %v", entries, err)
	}

	res, err := fake.Download(context.Background(), "u", DownloadOptions{Format: "best"}, nil)
	if err != nil || res.FilePath != "/f" || res.FileSize != 7 {
		t.Errorf("Download() = %+v, %v", res, err)
	}
}

func TestFakeRunnerNilFuncs(t *testing.T) {
	fake := &FakeRunner{}

	entries, err := fake.Index(context.Background(), "u", IndexOptions{})
	if err != nil || entries != nil {
		t.Errorf("Index() with nil func = %v, %v; want nil, nil", entries, err)
	}

	res, err := fake.Download(context.Background(), "u", DownloadOptions{}, nil)
	if err != nil || res != (DownloadResult{}) {
		t.Errorf("Download() with nil func = %+v, %v; want zero, nil", res, err)
	}
}
