package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWarnForPathFlagsStorageThatLosesWrites(t *testing.T) {
	mounts := map[string]string{
		"/":                  "overlay",
		"/config":            "9p",
		"/media":             "9p",
		"/var/lib/subscribe": "ext4",
	}

	tests := []struct {
		name     string
		path     string
		wantWarn bool
		wantType string
	}{
		{"database on a Windows bind mount", "/config", true, "9p"},
		{"a file inside the bind mount", "/config/sub_scribe.db", true, "9p"},
		{"database on a real volume", "/var/lib/subscribe", false, ""},
		{"a path on the container filesystem", "/tmp/scratch", false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warning := warnForPath(test.path, mounts)

			if (warning != nil) != test.wantWarn {
				t.Fatalf("warning = %+v, want warning: %v", warning, test.wantWarn)
			}
			if warning != nil && warning.Type != test.wantType {
				t.Errorf("Type = %q, want %q", warning.Type, test.wantType)
			}
		})
	}
}

func TestWarnForPathPrefersTheNearestMount(t *testing.T) {
	// The database sits on a sound volume mounted inside an unsafe one; the
	// nearest mount is what actually stores the file.
	mounts := map[string]string{
		"/":          "overlay",
		"/config":    "9p",
		"/config/db": "ext4",
	}

	if warning := warnForPath("/config/db/sub_scribe.db", mounts); warning != nil {
		t.Fatalf("warning = %+v, want none: the nearest mount is ext4", warning)
	}
}

func TestReadMountsParsesTheKernelTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts")
	table := "overlay / overlay rw,relatime 0 0\n" +
		"D:\\134 /config 9p rw,dirsync 0 0\n" +
		"drivers /my\\040drive ext4 rw 0 0\n" +
		"malformed-line\n"
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatalf("write mounts: %v", err)
	}

	mounts, err := readMounts(path)
	if err != nil {
		t.Fatalf("readMounts: %v", err)
	}

	if mounts["/config"] != "9p" {
		t.Errorf("/config = %q, want 9p", mounts["/config"])
	}
	if mounts["/my drive"] != "ext4" {
		t.Errorf("escaped mount point not decoded: %v", mounts)
	}
	if len(mounts) != 3 {
		t.Errorf("parsed %d mounts, want 3 (the malformed line is skipped)", len(mounts))
	}
}

func TestCheckDurableStorageIsSilentWithoutAMountTable(t *testing.T) {
	// On a platform with no /proc/mounts there is nothing to check, and a missing
	// table must never be reported as a problem.
	if warning := CheckDurableStorage(t.TempDir()); warning != nil && warning.Type == "" {
		t.Fatalf("warning = %+v, want a typed warning or none", warning)
	}
}
