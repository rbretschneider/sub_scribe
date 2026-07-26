package store

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mountInfoPath lists the mounted filesystems on Linux. On other platforms it is
// absent and the check simply reports nothing.
const mountInfoPath = "/proc/mounts"

// unsafeFilesystems are network and virtualisation filesystems that do not
// reliably honour fsync ordering. SQLite's durability guarantees rest entirely on
// that contract: when it is broken, a hard stop can leave committed rows missing
// while rows written after them survive — a database that passes integrity_check
// yet has dangling foreign keys. The database belongs on a real local
// filesystem; a Docker named volume is the usual fix for a bind mount.
var unsafeFilesystems = map[string]string{
	"9p":            "a Windows or macOS bind mount (Docker Desktop)",
	"virtiofs":      "a host bind mount (Docker Desktop)",
	"fuse.grpcfuse": "a host bind mount (Docker Desktop)",
	"nfs":           "a network share",
	"nfs4":          "a network share",
	"cifs":          "a Windows network share",
	"smbfs":         "a network share",
	"fuseblk":       "a FUSE-mounted volume",
}

// FilesystemWarning describes a data directory sitting on storage that cannot be
// trusted to keep committed writes.
type FilesystemWarning struct {
	// Path is the directory that was checked.
	Path string
	// Type is the filesystem's kernel name, e.g. "9p".
	Type string
	// Description explains the filesystem in the terms a user recognises.
	Description string
}

// CheckDurableStorage reports whether path sits on a filesystem known to lose
// committed writes, so the caller can warn loudly at startup rather than let the
// database quietly shed rows. It returns nil when the storage looks sound, when
// the platform exposes no mount table, or when the mount cannot be determined —
// an unrecognised filesystem is not evidence of a problem.
func CheckDurableStorage(path string) *FilesystemWarning {
	mounts, err := readMounts(mountInfoPath)
	if err != nil {
		return nil
	}
	return warnForPath(path, mounts)
}

// warnForPath matches a path against a mount table, using the longest matching
// mount point so a nested mount wins over its parent.
func warnForPath(path string, mounts map[string]string) *FilesystemWarning {
	resolved := filepath.ToSlash(filepath.Clean(path))

	points := make([]string, 0, len(mounts))
	for point := range mounts {
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool { return len(points[i]) > len(points[j]) })

	for _, point := range points {
		if !isUnder(resolved, point) {
			continue
		}
		fsType := mounts[point]
		description, unsafe := unsafeFilesystems[fsType]
		if !unsafe {
			return nil
		}
		return &FilesystemWarning{Path: path, Type: fsType, Description: description}
	}
	return nil
}

// isUnder reports whether path lies at or beneath mount point.
func isUnder(path, point string) bool {
	if path == point {
		return true
	}
	if point == "/" {
		return true
	}
	return strings.HasPrefix(path, point+"/")
}

// readMounts parses a /proc/mounts-style table into mount point -> filesystem
// type.
func readMounts(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	mounts := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// device, mount point, type, ...
		if len(fields) < 3 {
			continue
		}
		mounts[unescapeMountPoint(fields[1])] = fields[2]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

// unescapeMountPoint decodes the octal escapes the kernel uses for spaces and
// other awkward characters in mount points.
func unescapeMountPoint(point string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(point)
}
