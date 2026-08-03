// Package artwork saves a source's channel imagery into its show folder, in the
// filenames media servers look for. It is the only package that fetches images
// over HTTP; everything above it deals in URLs.
package artwork

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"sub_scribe/internal/domain"
)

const (
	// posterName is the series poster at a show folder's root. Plex accepts
	// "folder", "poster", or "show"; Jellyfin and Kodi read "poster". It is the
	// one name all three agree on.
	posterName = "poster.jpg"
	// backgroundName is the series backdrop. Plex accepts "art", "backdrop",
	// "background", or "fanart", and "fanart" is what Kodi and Jellyfin expect.
	backgroundName = "fanart.jpg"
	// seasonPosterPrefix begins a season poster's filename, which lives inside the
	// season folder and carries the season number with no separator:
	// "Season 2026/Season2026.jpg".
	seasonPosterPrefix = "Season"
	seasonPosterSuffix = ".jpg"
)

const (
	// fileMode and dirMode match the rest of the library's output.
	fileMode os.FileMode = 0o644
	dirMode  os.FileMode = 0o755

	// maxImageBytes caps what will be read from a URL. Channel art is tens of
	// kilobytes; anything approaching this is not artwork, and streaming it into
	// memory unbounded would be a way to be handed an out-of-memory error by a
	// remote server.
	maxImageBytes = 16 << 20

	// requestTimeout bounds a single image fetch. Artwork is a nicety fetched on
	// the download path, so it must never be what makes a download hang.
	requestTimeout = 30 * time.Second
)

// errShowDirRequired reports a call with no show folder to write into.
var errShowDirRequired = errors.New("artwork: show directory must not be empty")

// Writer downloads channel imagery and writes it beside a show's media.
type Writer struct {
	client *http.Client
}

// NewWriter constructs a Writer using client for image fetches. A nil client
// gets one carrying the package's own timeout, so a caller that has no opinion
// still cannot be left waiting forever.
func NewWriter(client *http.Client) *Writer {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Writer{client: client}
}

// NeedsArt reports whether showDir is missing any of the artwork it should have:
// the series poster, or a poster for any season folder present.
//
// This exists so the caller can skip asking the provider for URLs it will not
// use. Artwork is refreshed on a path that runs after every download and at
// every startup, and a few stat calls are the difference between that being free
// and it being a network round-trip per channel per download.
//
// A missing backdrop deliberately does not count. Many channels publish no
// banner at all, and treating "no backdrop" as "not done yet" would ask the
// provider for one forever.
func (w *Writer) NeedsArt(showDir string) bool {
	if showDir == "" {
		return false
	}
	if !fileExists(filepath.Join(showDir, posterName)) {
		return true
	}
	for _, dir := range seasonDirs(showDir) {
		if !fileExists(seasonPosterPath(showDir, dir)) {
			return true
		}
	}
	return false
}

// WriteArt saves art into showDir, reporting whether anything was written.
//
// Existing files are left alone rather than refetched: the images do not change,
// and rewriting them would restamp the folder and invite a watching media server
// to rescan the series every time a download finishes.
func (w *Writer) WriteArt(ctx context.Context, showDir string, art domain.ChannelArtwork) (bool, error) {
	if showDir == "" {
		return false, errShowDirRequired
	}
	if art.IsEmpty() {
		return false, nil
	}
	if err := os.MkdirAll(showDir, dirMode); err != nil {
		return false, fmt.Errorf("create show directory %q: %w", showDir, err)
	}

	poster, wrotePoster, err := w.ensureFile(ctx, filepath.Join(showDir, posterName), art.PosterURL)
	if err != nil {
		return false, err
	}
	wroteBackground, err := w.ensureBackground(ctx, showDir, art.BackgroundURL)
	if err != nil {
		return wrotePoster, err
	}
	wroteSeasons, err := writeSeasonPosters(showDir, poster)
	return wrotePoster || wroteBackground || wroteSeasons, err
}

// ensureBackground writes the series backdrop, discarding the bytes: unlike the
// poster, nothing else is derived from it.
func (w *Writer) ensureBackground(ctx context.Context, showDir, url string) (bool, error) {
	_, wrote, err := w.ensureFile(ctx, filepath.Join(showDir, backgroundName), url)
	return wrote, err
}

// ensureFile makes sure path holds the image at url, returning the image's bytes
// along with whether they had to be fetched.
//
// An already-present file is read back rather than re-downloaded, which is what
// lets a season poster be filled in later from the poster already on disk
// without going back to the network.
func (w *Writer) ensureFile(ctx context.Context, path, url string) ([]byte, bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		return existing, false, nil
	}
	if url == "" {
		return nil, false, nil
	}
	body, err := w.fetch(ctx, url)
	if err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, body, fileMode); err != nil {
		return nil, false, fmt.Errorf("write artwork to %q: %w", path, err)
	}
	return body, true, nil
}

// fetch downloads an image, refusing anything that is not a successful response
// carrying a body.
func (w *Writer) fetch(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build artwork request for %q: %w", url, err)
	}
	response, err := w.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch artwork %q: %w", url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch artwork %q: unexpected status %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes))
	if err != nil {
		return nil, fmt.Errorf("read artwork %q: %w", url, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("fetch artwork %q: empty response", url)
	}
	return body, nil
}

// writeSeasonPosters gives every season folder the show's poster, reporting
// whether any were added.
//
// A media server shows a placeholder for a season it has no image for, so
// without this a correctly-postered show still reads as half-broken the moment
// you open it. The show poster is reused because a YouTube channel has nothing
// per-season to offer — there are no real seasons, only years.
func writeSeasonPosters(showDir string, poster []byte) (bool, error) {
	if len(poster) == 0 {
		return false, nil
	}
	wrote := false
	for _, dir := range seasonDirs(showDir) {
		path := seasonPosterPath(showDir, dir)
		if fileExists(path) {
			continue
		}
		if err := os.WriteFile(path, poster, fileMode); err != nil {
			return wrote, fmt.Errorf("write season poster to %q: %w", path, err)
		}
		wrote = true
	}
	return wrote, nil
}

// seasonDirs returns the names of showDir's season folders. A folder that does
// not follow the "Season <number>" convention is not one a media server reads as
// a season either, so it is left alone.
func seasonDirs(showDir string) []string {
	entries, err := os.ReadDir(showDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && seasonNumberOf(entry.Name()) != "" {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs
}

// seasonPosterPath is where a season folder's poster belongs: inside the folder,
// named for its season number, which is where Plex looks for it.
func seasonPosterPath(showDir, seasonDir string) string {
	return filepath.Join(showDir, seasonDir, seasonPosterPrefix+seasonNumberOf(seasonDir)+seasonPosterSuffix)
}

// seasonDirPattern matches a season folder such as "Season 2026" or "Season01",
// capturing the number a media server reads as the season.
var seasonDirPattern = regexp.MustCompile(`(?i)^season[ _-]?(\d{1,4})$`)

// seasonNumberOf extracts the season number from a folder name such as
// "Season 2026", returning empty when the name is not a season folder.
func seasonNumberOf(dirName string) string {
	match := seasonDirPattern.FindStringSubmatch(dirName)
	if match == nil {
		return ""
	}
	return match[1]
}

// fileExists reports whether path is present and readable as a file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
