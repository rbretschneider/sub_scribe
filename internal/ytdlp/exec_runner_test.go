package ytdlp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScanIndexEntries(t *testing.T) {
	output := strings.Join([]string{
		`{"id":"a","title":"A"}`,
		``,
		`not-json-should-be-skipped`,
		`{"id":"b","title":"B","duration":10}`,
	}, "\n")

	got := scanIndexEntries(strings.NewReader(output))
	if len(got) != 2 {
		t.Fatalf("scanIndexEntries() returned %d entries, want 2", len(got))
	}
	if got[0].ExternalID != "a" || got[1].ExternalID != "b" {
		t.Errorf("scanIndexEntries() ids = %q,%q; want a,b", got[0].ExternalID, got[1].ExternalID)
	}
	if got[1].Duration != 10*time.Second {
		t.Errorf("scanIndexEntries() duration = %v, want 10s", got[1].Duration)
	}
}

func TestScanDownloadOutput(t *testing.T) {
	output := strings.Join([]string{
		"[youtube] extracting",
		progressLinePrefix + "10.0%",
		progressLinePrefix + "55.5%",
		progressLinePrefix + "100.0%",
		"" + afterMovePrintPrefix + "/media/final.mp4",
		heightPrintPrefix + "1080",
	}, "\n")

	var percents []float64
	filePath, height := scanDownloadOutput(strings.NewReader(output), func(p float64) {
		percents = append(percents, p)
	})

	if filePath != "/media/final.mp4" {
		t.Errorf("scanDownloadOutput() path = %q, want /media/final.mp4", filePath)
	}
	if height != 1080 {
		t.Errorf("scanDownloadOutput() height = %d, want 1080", height)
	}
	want := []float64{10.0, 55.5, 100.0}
	if len(percents) != len(want) {
		t.Fatalf("scanDownloadOutput() got %d progress calls, want %d", len(percents), len(want))
	}
	for i, p := range want {
		if percents[i] != p {
			t.Errorf("progress[%d] = %v, want %v", i, percents[i], p)
		}
	}
}

func TestScanDownloadOutputNilProgress(t *testing.T) {
	output := "download:50.0%\n" + afterMovePrintPrefix + "/x.mp4"
	filePath, height := scanDownloadOutput(strings.NewReader(output), nil)
	if filePath != "/x.mp4" {
		t.Errorf("scanDownloadOutput() path = %q, want /x.mp4", filePath)
	}
	if height != 0 {
		t.Errorf("scanDownloadOutput() height = %d, want 0 when none is printed", height)
	}
}

func TestScanDownloadOutputAudioOnlyHeightIsUnknown(t *testing.T) {
	// yt-dlp prints "NA" for %(height)s on audio-only downloads; that must read
	// as unknown, never as a zero-height video.
	output := afterMovePrintPrefix + "/x.m4a\n" + heightPrintPrefix + "NA"
	_, height := scanDownloadOutput(strings.NewReader(output), nil)
	if height != 0 {
		t.Errorf("scanDownloadOutput() height = %d, want 0 for NA", height)
	}
}

func TestBuildDownloadResultEmptyPathIsFilteredOut(t *testing.T) {
	// No path reported and no destination to check: the item really was declined.
	_, err := buildDownloadResult("https://x/v", "", 0, DownloadOptions{})
	if !errors.Is(err, ErrFilteredOut) {
		t.Errorf("empty path err = %v, want ErrFilteredOut", err)
	}
}
