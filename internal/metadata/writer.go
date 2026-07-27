package metadata

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// nfoExtension is the sidecar file extension for Kodi/Jellyfin metadata.
const nfoExtension = ".nfo"

// nfoFileMode is the permission mode for written sidecar files.
const nfoFileMode os.FileMode = 0o644

// Writer writes Kodi/Jellyfin .nfo sidecar files next to downloaded media.
type Writer struct{}

// Compile-time assertion that Writer satisfies the library port.
var _ library.MetadataWriter = (*Writer)(nil)

// NewWriter constructs a Writer.
func NewWriter() *Writer {
	return &Writer{}
}

// WriteFor builds the NFO for media in the requested format and writes it beside
// mediaFilePath, replacing that path's extension with ".nfo". sourceName is
// recorded as the studio. The context is honoured before performing the write.
func (w *Writer) WriteFor(ctx context.Context, mediaFilePath string, media domain.Media, sourceName string, format domain.MetadataFormat) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write nfo for %q: %w", mediaFilePath, err)
	}
	// The numbering comes from the file name so the two can never contradict
	// each other; when the name carries no token, the NFO simply omits it.
	numbering, _ := ParseSeasonEpisode(mediaFilePath)
	body, err := BuildNFO(media, sourceName, format, numbering)
	if err != nil {
		return fmt.Errorf("write nfo for %q: %w", mediaFilePath, err)
	}
	nfoPath := nfoPathFor(mediaFilePath)
	if err := os.WriteFile(nfoPath, body, nfoFileMode); err != nil {
		return fmt.Errorf("write nfo to %q: %w", nfoPath, err)
	}
	return nil
}

// showNFOName is the series-level file media servers look for at a show's root.
const showNFOName = "tvshow.nfo"

// WriteShow writes the series-level NFO at a channel's folder root, naming the
// series explicitly so a media server has no reason to guess at it.
//
// It rewrites the file only when the content would change. The file is tiny, but
// touching it on every download would restamp its modification time and invite
// the media server to rescan the series each time.
func (w *Writer) WriteShow(ctx context.Context, showDir, name, url string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write show nfo in %q: %w", showDir, err)
	}
	body, err := BuildShowNFO(name, url)
	if err != nil {
		return fmt.Errorf("write show nfo in %q: %w", showDir, err)
	}
	path := filepath.Join(showDir, showNFOName)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
		return nil
	}
	if err := os.MkdirAll(showDir, showDirMode); err != nil {
		return fmt.Errorf("create show directory %q: %w", showDir, err)
	}
	if err := os.WriteFile(path, body, nfoFileMode); err != nil {
		return fmt.Errorf("write show nfo to %q: %w", path, err)
	}
	return nil
}

// showDirMode is the permission mode for a created show directory.
const showDirMode os.FileMode = 0o755

// nfoPathFor returns mediaFilePath with its extension replaced by ".nfo".
func nfoPathFor(mediaFilePath string) string {
	ext := filepath.Ext(mediaFilePath)
	base := strings.TrimSuffix(mediaFilePath, ext)
	return base + nfoExtension
}
