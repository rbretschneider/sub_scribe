package metadata

import (
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
	body, err := BuildNFO(media, sourceName, format)
	if err != nil {
		return fmt.Errorf("write nfo for %q: %w", mediaFilePath, err)
	}
	nfoPath := nfoPathFor(mediaFilePath)
	if err := os.WriteFile(nfoPath, body, nfoFileMode); err != nil {
		return fmt.Errorf("write nfo to %q: %w", nfoPath, err)
	}
	return nil
}

// nfoPathFor returns mediaFilePath with its extension replaced by ".nfo".
func nfoPathFor(mediaFilePath string) string {
	ext := filepath.Ext(mediaFilePath)
	base := strings.TrimSuffix(mediaFilePath, ext)
	return base + nfoExtension
}
