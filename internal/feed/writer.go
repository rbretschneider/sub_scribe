package feed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// feedFilePerm and feedDirPerm are the permissions for written feeds and their
// containing directory.
const (
	feedFilePerm = 0o644
	feedDirPerm  = 0o755
)

// Writer persists a source's RSS feed to a file under feedDir. It satisfies
// library.FeedWriter.
type Writer struct {
	feedDir string
}

// Ensure Writer implements the port the application core depends on.
var _ library.FeedWriter = (*Writer)(nil)

// NewWriter returns a Writer that writes feeds into feedDir.
func NewWriter(feedDir string) *Writer {
	return &Writer{feedDir: feedDir}
}

// WriteFeed builds the source's RSS feed and writes it to
// feedDir/<source.ID>.xml, creating feedDir if necessary.
func (w *Writer) WriteFeed(ctx context.Context, source domain.Source, items []domain.Media) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := BuildRSS(source, items)
	if err != nil {
		return fmt.Errorf("build rss for source %d: %w", source.ID, err)
	}
	if err := os.MkdirAll(w.feedDir, feedDirPerm); err != nil {
		return fmt.Errorf("create feed dir %q: %w", w.feedDir, err)
	}
	path := filepath.Join(w.feedDir, fmt.Sprintf("%d.xml", source.ID))
	if err := os.WriteFile(path, data, feedFilePerm); err != nil {
		return fmt.Errorf("write feed %q: %w", path, err)
	}
	return nil
}
