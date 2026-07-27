package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
)

// writeFile creates a file with some content, failing the test if it cannot.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// exists reports whether a path is present.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestApplyNamingTemplateMovesFilesAndSidecars is the whole point of the
// feature: changing a template only affects future downloads, so a library that
// outlived the change is split across two layouts. Media servers identify
// content from the filename, so the old-layout files stay permanently
// mis-titled no matter how often the library is rescanned.
func TestApplyNamingTemplateMovesFilesAndSidecars(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	// A file written under an older template, with the sidecars of its day.
	oldBase := filepath.Join(h.mediaDir, "My Channel", "My Channel - 2026-03-05 - Old Layout [abc123]")
	writeFile(t, oldBase+".mkv")
	writeFile(t, oldBase+".jpg")
	writeFile(t, oldBase+".nfo")
	writeFile(t, oldBase+".en.srt")

	mediaID, _ := h.media.Upsert(ctx, domain.Media{
		SourceID:   src.ID,
		ExternalID: "abc123",
		Status:     domain.MediaDownloaded,
		FilePath:   oldBase + ".mkv",
		Metadata:   domain.MediaMetadata{Title: "Old Layout", UploadDate: h.now},
	})

	report, err := h.svc.ApplyNamingTemplate(ctx, src.ID)
	if err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}
	if report.Renamed != 1 {
		t.Fatalf("renamed = %d, want 1 (report: %+v)", report.Renamed, report)
	}

	// The harness profile's template is "{{ source_name }}/{{ title }}".
	newBase := filepath.Join(h.mediaDir, "My Channel", "Old Layout")
	for _, ext := range []string{".mkv", ".jpg", ".nfo", ".en.srt"} {
		if !exists(newBase + ext) {
			t.Errorf("%s was not moved to the new layout", ext)
		}
		if exists(oldBase + ext) {
			t.Errorf("%s was left behind at the old path", ext)
		}
	}

	// A recorded path that no longer points at the file is the same failure the
	// rename was meant to fix, one level down.
	got, _ := h.media.Get(ctx, mediaID)
	if got.FilePath != newBase+".mkv" {
		t.Errorf("recorded path = %q, want %q", got.FilePath, newBase+".mkv")
	}
}

func TestApplyNamingTemplateLeavesCorrectlyNamedFilesAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	path := filepath.Join(h.mediaDir, "My Channel", "Already Right.mkv")
	writeFile(t, path)
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "ok1", Status: domain.MediaDownloaded, FilePath: path,
		Metadata: domain.MediaMetadata{Title: "Already Right", UploadDate: h.now},
	})

	report, err := h.svc.ApplyNamingTemplate(ctx, src.ID)
	if err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}
	if report.Renamed != 0 {
		t.Errorf("renamed = %d, want 0; a matching file must not be touched", report.Renamed)
	}
	if report.Checked != 1 {
		t.Errorf("checked = %d, want 1", report.Checked)
	}
	if !exists(path) {
		t.Error("the correctly named file disappeared")
	}
}

// TestApplyNamingTemplateWillNotOverwrite guards the one way this feature could
// destroy an archive. Two items can render to the same name, and a rename that
// overwrote the occupant would silently delete a video.
func TestApplyNamingTemplateWillNotOverwrite(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	oldPath := filepath.Join(h.mediaDir, "My Channel", "old name.mkv")
	occupied := filepath.Join(h.mediaDir, "My Channel", "Clash.mkv")
	writeFile(t, oldPath)
	writeFile(t, occupied)

	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "c1", Status: domain.MediaDownloaded, FilePath: oldPath,
		Metadata: domain.MediaMetadata{Title: "Clash", UploadDate: h.now},
	})

	report, err := h.svc.ApplyNamingTemplate(ctx, src.ID)
	if err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}
	if report.Blocked != 1 {
		t.Errorf("blocked = %d, want 1", report.Blocked)
	}
	if !exists(occupied) || !exists(oldPath) {
		t.Error("a rename onto an occupied path destroyed a file")
	}
}

func TestApplyNamingTemplateIgnoresItemsWithNoFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "p1", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Not downloaded", UploadDate: h.now},
	})
	// Recorded as downloaded, but the file is gone — a separate inconsistency
	// that reconciliation owns; renaming must not invent a move for it.
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "g1", Status: domain.MediaDownloaded,
		FilePath: filepath.Join(h.mediaDir, "My Channel", "vanished.mkv"),
		Metadata: domain.MediaMetadata{Title: "Vanished", UploadDate: h.now},
	})

	report, err := h.svc.ApplyNamingTemplate(ctx, src.ID)
	if err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}
	if report.Renamed != 0 || report.Blocked != 0 {
		t.Errorf("report = %+v, want nothing renamed or blocked", report)
	}
	if report.Checked != 1 {
		t.Errorf("checked = %d, want 1 (only the downloaded item)", report.Checked)
	}
}

// TestCompanionMovesDoesNotClaimANeighboursFile pins the matching rule. Sidecars
// are found by name, and a looser match would drag an unrelated video along
// whenever one name happened to begin with another.
func TestCompanionMovesDoesNotClaimANeighboursFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Video.mkv"))
	writeFile(t, filepath.Join(dir, "Video.jpg"))
	writeFile(t, filepath.Join(dir, "Video 2.mkv"))
	writeFile(t, filepath.Join(dir, "Video Extended.mkv"))

	moves := companionMoves(filepath.Join(dir, "Video.mkv"), filepath.Join(dir, "New.mkv"))

	if len(moves) != 2 {
		t.Fatalf("moves = %d, want 2 (the media file and its .jpg): %+v", len(moves), moves)
	}
	for _, move := range moves {
		switch filepath.Base(move.from) {
		case "Video.mkv":
			if filepath.Base(move.to) != "New.mkv" {
				t.Errorf("media file goes to %q, want New.mkv", move.to)
			}
		case "Video.jpg":
			if filepath.Base(move.to) != "New.jpg" {
				t.Errorf("sidecar goes to %q, want New.jpg", move.to)
			}
		default:
			t.Errorf("claimed an unrelated file: %s", move.from)
		}
	}
}

func TestCompanionMovesKeepsMultiPartExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Clip [id].mkv"))
	writeFile(t, filepath.Join(dir, "Clip [id].en.srt"))

	moves := companionMoves(filepath.Join(dir, "Clip [id].mkv"), filepath.Join(dir, "s2026e010101 - Clip.mkv"))

	var found bool
	for _, move := range moves {
		if filepath.Base(move.to) == "s2026e010101 - Clip.en.srt" {
			found = true
		}
	}
	if !found {
		t.Errorf("the .en.srt subtitle lost its language suffix: %+v", moves)
	}
}

// TestApplyNamingTemplateBacksfillsTheShowSidecar covers the other half of what
// a media server needs. Correct filenames still leave it matching the folder
// name against an online database to work out what the series is; the
// series-level sidecar is what removes that guess.
func TestApplyNamingTemplateBacksfillsTheShowSidecar(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A series library, which is the only kind a show-level sidecar applies to.
	profileID, err := h.profiles.Create(ctx, domain.MediaProfile{
		Name:               "Plex TV",
		OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind:               domain.MediaVideo,
		QualityFormat:      "bestvideo+bestaudio",
		MetadataFormat:     domain.MetadataEpisode,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	path := filepath.Join(h.mediaDir, "My Channel", "Something.mkv")
	writeFile(t, path)
	h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "s1", Status: domain.MediaDownloaded, FilePath: path,
		Metadata: domain.MediaMetadata{Title: "Something", UploadDate: h.now},
	})

	if _, err := h.svc.ApplyNamingTemplate(ctx, src.ID); err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}

	if h.metadata.showCalls != 1 {
		t.Fatalf("show sidecar writes = %d, want 1", h.metadata.showCalls)
	}
	want := filepath.Join(h.mediaDir, "My Channel")
	if h.metadata.lastShowDir != want {
		t.Errorf("show sidecar written to %q, want the channel root %q", h.metadata.lastShowDir, want)
	}
	if h.metadata.lastShowName != "My Channel" {
		t.Errorf("show named %q, want the source name", h.metadata.lastShowName)
	}
}

// TestShowDirIsTheChannelFolderNotTheSeasonFolder pins where the sidecar goes. A
// media server looks for it at the series root; one written into "Season 2026"
// is simply never read.
func TestShowDirIsTheChannelFolderNotTheSeasonFolder(t *testing.T) {
	h := newHarness(t)
	got, ok := h.svc.showDirFor(filepath.Join(h.mediaDir, "Chan", "Season 2026", "s2026e010101 - X.mkv"))
	if !ok {
		t.Fatal("no show directory resolved for a normal channel/season layout")
	}
	if want := filepath.Join(h.mediaDir, "Chan"); got != want {
		t.Errorf("show dir = %q, want %q", got, want)
	}
}

// TestShowDirRejectsAFlatLayout: with the file sitting directly in the media
// root there is no series folder, and writing tvshow.nfo there would claim the
// whole library is one show.
func TestShowDirRejectsAFlatLayout(t *testing.T) {
	h := newHarness(t)
	if dir, ok := h.svc.showDirFor(filepath.Join(h.mediaDir, "loose.mkv")); ok {
		t.Errorf("resolved a show directory %q for a file in the media root", dir)
	}
}

// TestApplyNamingTemplateRefreshesEveryEpisodeSidecar covers the sidecars of
// files that were already correctly named. Those were written by an older
// version without the season and episode elements a media server needs, and
// nothing else would ever rewrite them.
func TestApplyNamingTemplateRefreshesEveryEpisodeSidecar(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(ctx, validInput(profileID))

	for _, title := range []string{"Already Right", "Also Fine"} {
		path := filepath.Join(h.mediaDir, "My Channel", title+".mkv")
		writeFile(t, path)
		h.media.Upsert(ctx, domain.Media{
			SourceID: src.ID, ExternalID: title, Status: domain.MediaDownloaded, FilePath: path,
			Metadata: domain.MediaMetadata{Title: title, UploadDate: h.now},
		})
	}

	if _, err := h.svc.ApplyNamingTemplate(ctx, src.ID); err != nil {
		t.Fatalf("ApplyNamingTemplate: %v", err)
	}
	if h.metadata.calls != 2 {
		t.Errorf("sidecar writes = %d, want 2 (one per downloaded item)", h.metadata.calls)
	}
}
