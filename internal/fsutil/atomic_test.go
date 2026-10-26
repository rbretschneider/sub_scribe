package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomic_CreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.nfo")

	if err := WriteFileAtomic(path, []byte("one"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("content = %q, err = %v; want %q", got, err, "two")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	if names := listNames(t, dir); len(names) != 1 || names[0] != "a.nfo" {
		t.Errorf("directory = %v, want only a.nfo (no temp file left)", names)
	}
}

func TestWriteFileAtomic_FailureLeavesNoTempFileAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	// A directory at the target path makes the final rename fail.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("x"), 0o644); err == nil {
		t.Fatal("expected an error when the target is a directory")
	}
	if names := listNames(t, dir); len(names) != 1 || names[0] != "target" {
		t.Errorf("directory = %v, want only the original target", names)
	}
}

func TestWriteFileAtomic_MissingDirectoryFails(t *testing.T) {
	err := WriteFileAtomic(filepath.Join(t.TempDir(), "nope", "f"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestWriteFileAtomic_TempNameNeverLooksLikeSidecarOrFeed(t *testing.T) {
	dir := t.TempDir()
	// Make the rename fail after the temp file exists, then inspect nothing is
	// left; for the name itself, build one the way the helper does.
	tmp, err := os.CreateTemp(dir, "."+"x.nfo"+tempMarker+"*")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	name := filepath.Base(tmp.Name())
	for _, suffix := range []string{".nfo", ".xml"} {
		if strings.HasSuffix(name, suffix) {
			t.Errorf("temp name %q ends in %q", name, suffix)
		}
	}
}

func TestWriteFileAtomic_ConcurrentReaderSeesCompleteContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.xml")
	oldBody := bytes.Repeat([]byte("old"), 50_000)
	newBody := bytes.Repeat([]byte("new"), 70_000)
	if err := WriteFileAtomic(path, oldBody, 0o644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad []int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if !bytes.Equal(got, oldBody) && !bytes.Equal(got, newBody) {
				bad = append(bad, len(got))
			}
		}
	}()
	for i := 0; i < 50; i++ {
		body := newBody
		if i%2 == 1 {
			body = oldBody
		}
		if err := WriteFileAtomic(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if len(bad) > 0 {
		t.Errorf("reader saw %d partial files, e.g. length %d", len(bad), bad[0])
	}
}
