// Package fsutil holds small filesystem helpers shared by the packages that
// write files other programs read while the application is running.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// tempMarker is embedded in temporary file names. The name starts with a dot and
// ends in random digits, so it never carries a ".nfo" or ".xml" suffix: a media
// server scanning the folder or the orphan sweep must not mistake a half-written
// temporary file for a real sidecar or feed.
const tempMarker = ".tmp-"

// WriteFileAtomic writes data to path so that a concurrent reader sees either the
// complete previous content or the complete new content, never a partial file.
//
// It writes to a temporary file in the same directory (a rename is only atomic
// within one filesystem), syncs it, sets the requested mode and renames it over
// the target. The temporary file is removed on every failure path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+tempMarker+"*")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file for %q: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file for %q: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %q: %w", path, err)
	}
	// CreateTemp makes the file private (0600); chmod after the write so the
	// final mode does not depend on the process umask.
	if err = os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("set mode on temp file for %q: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
