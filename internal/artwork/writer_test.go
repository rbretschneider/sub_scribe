package artwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sub_scribe/internal/domain"
)

// posterBytes and bannerBytes stand in for real images: the writer never decodes
// them, so distinct payloads are enough to prove which URL landed where.
var (
	posterBytes = []byte("poster-image-bytes")
	bannerBytes = []byte("banner-image-bytes")
)

// showHarness is a show folder on disk plus a server offering its channel art,
// so a test states only what it is actually about.
type showHarness struct {
	t       *testing.T
	dir     string
	writer  *Writer
	art     domain.ChannelArtwork
	served  map[string][]byte
	fetches int
}

// newShowHarness creates an empty show folder served by a poster and a banner.
func newShowHarness(t *testing.T) *showHarness {
	t.Helper()
	h := &showHarness{
		t:      t,
		dir:    filepath.Join(t.TempDir(), "Some Channel"),
		served: map[string][]byte{"/poster.jpg": posterBytes, "/banner.jpg": bannerBytes},
	}
	if err := os.MkdirAll(h.dir, dirMode); err != nil {
		t.Fatalf("create show dir: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fetches++
		body, ok := h.served[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	h.writer = NewWriter(server.Client())
	h.art = domain.ChannelArtwork{
		PosterURL:     server.URL + "/poster.jpg",
		BackgroundURL: server.URL + "/banner.jpg",
	}
	return h
}

// addSeason creates a season folder under the show.
func (h *showHarness) addSeason(name string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Join(h.dir, name), dirMode); err != nil {
		h.t.Fatalf("create season dir %q: %v", name, err)
	}
}

// write runs the writer over the show folder, failing the test on error.
func (h *showHarness) write() bool {
	h.t.Helper()
	changed, err := h.writer.WriteArt(context.Background(), h.dir, h.art)
	if err != nil {
		h.t.Fatalf("WriteArt: %v", err)
	}
	return changed
}

// requireFile asserts that a path relative to the show folder holds want.
func (h *showHarness) requireFile(relative string, want []byte) {
	h.t.Helper()
	got, err := os.ReadFile(filepath.Join(h.dir, relative))
	if err != nil {
		h.t.Fatalf("read %q: %v", relative, err)
	}
	if string(got) != string(want) {
		h.t.Errorf("%q = %q, want %q", relative, got, want)
	}
}

// requireAbsent asserts that a path relative to the show folder does not exist.
func (h *showHarness) requireAbsent(relative string) {
	h.t.Helper()
	if _, err := os.Stat(filepath.Join(h.dir, relative)); err == nil {
		h.t.Errorf("%q exists, want it not written", relative)
	}
}

// TestWriteArtSavesPosterAndBackground covers the show-level images, which are
// what a media server shows for the series itself.
func TestWriteArtSavesPosterAndBackground(t *testing.T) {
	h := newShowHarness(t)

	if !h.write() {
		t.Error("WriteArt reported no change, want the first write to count as one")
	}

	h.requireFile(posterName, posterBytes)
	h.requireFile(backgroundName, bannerBytes)
}

// TestWriteArtGivesEverySeasonAPoster covers the placeholder a media server
// shows for a season it has no image for — the show poster stands in, since a
// channel has nothing per-season to offer.
func TestWriteArtGivesEverySeasonAPoster(t *testing.T) {
	h := newShowHarness(t)
	h.addSeason("Season 2025")
	h.addSeason("Season 2026")

	h.write()

	h.requireFile(filepath.Join("Season 2025", "Season2025.jpg"), posterBytes)
	h.requireFile(filepath.Join("Season 2026", "Season2026.jpg"), posterBytes)
}

// TestWriteArtIgnoresNonSeasonFolders keeps the writer from scattering posters
// through folders a media server does not read as seasons.
func TestWriteArtIgnoresNonSeasonFolders(t *testing.T) {
	h := newShowHarness(t)
	h.addSeason("Extras")

	h.write()

	entries, err := os.ReadDir(filepath.Join(h.dir, "Extras"))
	if err != nil {
		t.Fatalf("read Extras: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Extras holds %d files, want it left alone", len(entries))
	}
}

// TestWriteArtLeavesExistingFilesAlone is the guard against restamping the
// library: rewriting artwork that is already correct reads to a watching media
// server as "this changed, rescan the series".
func TestWriteArtLeavesExistingFilesAlone(t *testing.T) {
	h := newShowHarness(t)
	h.addSeason("Season 2026")
	h.write()
	fetchesAfterFirst := h.fetches

	if h.write() {
		t.Error("WriteArt reported a change on the second pass, want none")
	}
	if h.fetches != fetchesAfterFirst {
		t.Errorf("fetches = %d, want no refetch after %d", h.fetches, fetchesAfterFirst)
	}
}

// TestWriteArtFillsANewSeasonFromTheExistingPoster covers next January: a new
// season folder appears long after the poster was saved, and the season image
// has to come from disk rather than another provider call.
func TestWriteArtFillsANewSeasonFromTheExistingPoster(t *testing.T) {
	h := newShowHarness(t)
	h.write()
	fetchesAfterFirst := h.fetches

	h.addSeason("Season 2027")
	if !h.write() {
		t.Error("WriteArt reported no change, want the new season poster to count")
	}

	h.requireFile(filepath.Join("Season 2027", "Season2027.jpg"), posterBytes)
	if h.fetches != fetchesAfterFirst {
		t.Errorf("fetches = %d, want the poster reused from disk", h.fetches)
	}
}

// TestWriteArtSkipsAMissingBackground covers the many channels that publish an
// avatar and no banner: the poster still has to land.
func TestWriteArtSkipsAMissingBackground(t *testing.T) {
	h := newShowHarness(t)
	h.art.BackgroundURL = ""

	h.write()

	h.requireFile(posterName, posterBytes)
	h.requireAbsent(backgroundName)
}

// TestWriteArtReportsAFailedFetch keeps a provider error from being mistaken for
// a channel that simply has no artwork.
func TestWriteArtReportsAFailedFetch(t *testing.T) {
	h := newShowHarness(t)
	h.served = nil

	if _, err := h.writer.WriteArt(context.Background(), h.dir, h.art); err == nil {
		t.Fatal("WriteArt succeeded, want an error for an unreachable image")
	}
}

// TestWriteArtRejectsAnEmptyShowDir validates the one input that has no sensible
// default: without a folder there is nowhere for the images to go.
func TestWriteArtRejectsAnEmptyShowDir(t *testing.T) {
	h := newShowHarness(t)

	if _, err := h.writer.WriteArt(context.Background(), "", h.art); err == nil {
		t.Fatal("WriteArt succeeded, want an error for an empty show directory")
	}
}

// TestWriteArtWithNoArtworkDoesNothing covers a collection the provider offers
// no imagery for at all.
func TestWriteArtWithNoArtworkDoesNothing(t *testing.T) {
	h := newShowHarness(t)

	changed, err := h.writer.WriteArt(context.Background(), h.dir, domain.ChannelArtwork{})
	if err != nil {
		t.Fatalf("WriteArt: %v", err)
	}
	if changed {
		t.Error("WriteArt reported a change, want none with nothing to write")
	}
	h.requireAbsent(posterName)
}

// TestNeedsArtIsTrueUntilEverythingIsOnDisk covers the check that keeps artwork
// off the network: it must stay true while anything is missing and go false once
// the folder is complete, or every download pays for a provider call.
func TestNeedsArtIsTrueUntilEverythingIsOnDisk(t *testing.T) {
	h := newShowHarness(t)
	h.addSeason("Season 2026")

	if !h.writer.NeedsArt(h.dir) {
		t.Error("NeedsArt = false on an empty show folder, want true")
	}
	h.write()
	if h.writer.NeedsArt(h.dir) {
		t.Error("NeedsArt = true after writing, want false")
	}
}

// TestNeedsArtIsTrueForASeasonAddedLater is what makes a show that gained a
// season since the last write get an image for it.
func TestNeedsArtIsTrueForASeasonAddedLater(t *testing.T) {
	h := newShowHarness(t)
	h.write()

	h.addSeason("Season 2027")

	if !h.writer.NeedsArt(h.dir) {
		t.Error("NeedsArt = false with an unpostered season, want true")
	}
}

// TestNeedsArtIgnoresAMissingBackground stops a channel that publishes no banner
// from being asked for one on every single download, forever.
func TestNeedsArtIgnoresAMissingBackground(t *testing.T) {
	h := newShowHarness(t)
	h.art.BackgroundURL = ""
	h.write()

	if h.writer.NeedsArt(h.dir) {
		t.Error("NeedsArt = true with only the backdrop missing, want false")
	}
}
