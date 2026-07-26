package naming

import (
	"regexp"
	"strings"
)

// maxComponentLength caps a single path segment to stay well under common
// filesystem limits (255 bytes) while leaving room for extensions and suffixes.
const maxComponentLength = 200

// emptyComponentFallback replaces a segment that sanitizes to nothing, so a
// missing title never yields an empty directory or filename.
const emptyComponentFallback = "_"

// illegalChars matches characters disallowed in path segments on Windows (the
// strictest common target, since media often lands on a NAS/SMB share Plex
// reads). Includes the path separators so a value like "AC/DC" cannot inject a
// directory boundary. Control characters are handled separately.
var illegalChars = regexp.MustCompile(`[<>:"/\\|?*]`)

// controlChars matches ASCII control characters (0x00–0x1F) that break many
// filesystems and tools.
var controlChars = regexp.MustCompile(`[\x00-\x1f]`)

// multiSpace collapses runs of whitespace introduced by removing illegal chars.
var multiSpace = regexp.MustCompile(`\s+`)

// windowsReserved is the set of Windows device names that are invalid as a whole
// path component regardless of extension.
var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// sanitizeComponent makes an arbitrary metadata value safe to use as a single
// path segment across Windows, macOS, and Linux. It never returns an empty
// string and never returns a segment containing a path separator.
func sanitizeComponent(value string) string {
	cleaned := illegalChars.ReplaceAllString(value, " ")
	cleaned = controlChars.ReplaceAllString(cleaned, "")
	cleaned = multiSpace.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.Trim(cleaned, ".")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return emptyComponentFallback
	}
	if _, reserved := windowsReserved[strings.ToUpper(cleaned)]; reserved {
		cleaned = emptyComponentFallback + cleaned
	}
	if len(cleaned) > maxComponentLength {
		cleaned = strings.TrimSpace(cleaned[:maxComponentLength])
	}
	return cleaned
}
