package ytdlp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates a file with some content under dir, creating parents.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAlreadyDownloadedFileIsNotMistakenForAFilteredOne(t *testing.T) {
	// yt-dlp prints no moved-file path when the file is already on disk from an
	// interrupted attempt. Reading that as "filtered out" marked finished
	// downloads as skipped after every restart.
	home := t.TempDir()
	relative := filepath.Join("Computerphile", "Season 2026", "GPS Hidden Messages [2Q6OvYjOJi0]")
	writeFile(t, filepath.Join(home, relative+".mkv"))

	result, err := buildDownloadResult("https://example.com/v", "",
		DownloadOptions{OutputPath: relative, HomeDir: home})
	if err != nil {
		t.Fatalf("buildDownloadResult: %v", err)
	}

	if result.FilePath != filepath.Join(home, relative+".mkv") {
		t.Errorf("FilePath = %q, want the file already on disk", result.FilePath)
	}
	if result.FileSize == 0 {
		t.Error("FileSize = 0, want the size of the existing file")
	}
}

func TestNothingOnDiskIsStillReportedAsFiltered(t *testing.T) {
	home := t.TempDir()
	relative := filepath.Join("Chan", "Season 2026", "An Old Video [abc]")
	if err := os.MkdirAll(filepath.Join(home, "Chan", "Season 2026"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := buildDownloadResult("https://example.com/v", "",
		DownloadOptions{OutputPath: relative, HomeDir: home})

	if !errors.Is(err, ErrFilteredOut) {
		t.Fatalf("err = %v, want ErrFilteredOut when the item really was declined", err)
	}
}

func TestPartialAndSidecarFilesDoNotCountAsADownload(t *testing.T) {
	home := t.TempDir()
	relative := filepath.Join("Chan", "Video [xyz]")
	for _, suffix := range []string{".mkv.part", ".webp", ".temp.mkv", ".f399.mp4.part"} {
		writeFile(t, filepath.Join(home, relative+suffix))
	}

	_, err := buildDownloadResult("https://example.com/v", "",
		DownloadOptions{OutputPath: relative, HomeDir: home})

	if !errors.Is(err, ErrFilteredOut) {
		t.Fatalf("err = %v, want ErrFilteredOut — none of those are a finished download", err)
	}
}

func TestBracketsInTitlesAreMatchedLiterally(t *testing.T) {
	// Every filename carries a "[videoid]" suffix, which glob would read as a
	// character class and fail to match.
	home := t.TempDir()
	relative := filepath.Join("Chan", "Tricky [a-z] Title [xyz123]")
	writeFile(t, filepath.Join(home, relative+".mp4"))

	result, err := buildDownloadResult("https://example.com/v", "",
		DownloadOptions{OutputPath: relative, HomeDir: home})
	if err != nil {
		t.Fatalf("buildDownloadResult: %v", err)
	}
	if filepath.Base(result.FilePath) != "Tricky [a-z] Title [xyz123].mp4" {
		t.Errorf("FilePath = %q, want the bracketed filename", result.FilePath)
	}
}

func TestAmbiguousLeftoversAreNotGuessedAt(t *testing.T) {
	home := t.TempDir()
	relative := filepath.Join("Chan", "Video [xyz]")
	writeFile(t, filepath.Join(home, relative+".mkv"))
	writeFile(t, filepath.Join(home, relative+".mp4"))

	_, err := buildDownloadResult("https://example.com/v", "",
		DownloadOptions{OutputPath: relative, HomeDir: home})

	if !errors.Is(err, ErrFilteredOut) {
		t.Fatalf("err = %v, want ErrFilteredOut rather than a guess between two files", err)
	}
}

func TestReportedPathIsAlwaysPreferredOverASearch(t *testing.T) {
	home := t.TempDir()
	relative := filepath.Join("Chan", "Video [xyz]")
	reported := filepath.Join(home, relative+".mkv")
	writeFile(t, reported)
	writeFile(t, filepath.Join(home, relative+".mp4"))

	result, err := buildDownloadResult("https://example.com/v", reported,
		DownloadOptions{OutputPath: relative, HomeDir: home})
	if err != nil {
		t.Fatalf("buildDownloadResult: %v", err)
	}
	if result.FilePath != reported {
		t.Errorf("FilePath = %q, want the path yt-dlp reported (%q)", result.FilePath, reported)
	}
}
