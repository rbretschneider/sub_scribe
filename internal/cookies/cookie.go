// Package cookies parses and assesses Netscape-format cookie files (cookies.txt)
// so the UI can tell a user, in plain language, whether their YouTube login is
// valid and when it expires. It treats cookie values as secrets: assessments
// expose metadata only, never the values themselves.
package cookies

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// netscapeFieldCount is the number of tab-separated fields in a cookies.txt row:
// domain, includeSubdomains, path, secure, expiration, name, value.
const netscapeFieldCount = 7

// httpOnlyPrefix marks a cookie line that would otherwise look like a comment.
// yt-dlp and browsers emit HttpOnly cookies with this prefix on the domain.
const httpOnlyPrefix = "#HttpOnly_"

// Cookie is one parsed entry from a cookies.txt file. Value is retained only for
// round-tripping to yt-dlp and is never surfaced in assessments or logs.
type Cookie struct {
	Domain  string
	Path    string
	Secure  bool
	Expires time.Time // zero for a session cookie (expiration field of 0)
	Name    string
	Value   string
}

// IsSession reports whether the cookie has no persistent expiry.
func (c Cookie) IsSession() bool { return c.Expires.IsZero() }

// Jar is a parsed collection of cookies with assessment behavior.
type Jar []Cookie

// Parse reads a Netscape-format cookie file. It tolerates comments, blank lines,
// and the #HttpOnly_ prefix, and fails only when a non-comment line is
// structurally malformed, so a user pasting a slightly ragged export still gets
// a clear error rather than silent data loss.
func Parse(r io.Reader) (Jar, error) {
	var jar Jar
	scanner := bufio.NewScanner(r)
	// Cookie values can be long; raise the line limit above bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		cookie, skip, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("cookies: line %d: %w", lineNum, err)
		}
		if skip {
			continue
		}
		jar = append(jar, cookie)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cookies: reading file: %w", err)
	}
	return jar, nil
}

// parseLine parses a single file line. It reports skip=true for blank lines and
// genuine comments, and returns a Cookie for data rows (including HttpOnly rows).
func parseLine(line string) (cookie Cookie, skip bool, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Cookie{}, true, nil
	}
	isHTTPOnly := strings.HasPrefix(trimmed, httpOnlyPrefix)
	if strings.HasPrefix(trimmed, "#") && !isHTTPOnly {
		return Cookie{}, true, nil
	}
	if isHTTPOnly {
		trimmed = strings.TrimPrefix(trimmed, httpOnlyPrefix)
	}

	fields := strings.Split(trimmed, "\t")
	if len(fields) != netscapeFieldCount {
		return Cookie{}, false, fmt.Errorf("expected %d tab-separated fields, got %d", netscapeFieldCount, len(fields))
	}

	expires, err := parseExpiry(fields[4])
	if err != nil {
		return Cookie{}, false, fmt.Errorf("invalid expiry %q: %w", fields[4], err)
	}
	return Cookie{
		Domain:  fields[0],
		Path:    fields[2],
		Secure:  strings.EqualFold(fields[3], "TRUE"),
		Expires: expires,
		Name:    fields[5],
		Value:   fields[6],
	}, false, nil
}

// parseExpiry converts the Unix-seconds expiration field into a time. A value of
// 0 denotes a session cookie and yields the zero time.
func parseExpiry(field string) (time.Time, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	if secs == 0 {
		return time.Time{}, nil
	}
	return time.Unix(secs, 0).UTC(), nil
}
