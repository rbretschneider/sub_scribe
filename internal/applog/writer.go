package applog

import (
	"io"
	"strings"
	"time"
)

// Writer adapts the standard library log package into the buffer: each written
// line is captured as a record and also forwarded to a passthrough writer
// (normally stdout), so log.Printf output shows up in the in-app viewer too.
type Writer struct {
	buf         *Buffer
	passthrough io.Writer
	now         func() time.Time
}

// NewWriter builds a Writer that captures into buf and forwards to passthrough.
func NewWriter(buf *Buffer, passthrough io.Writer) *Writer {
	return &Writer{buf: buf, passthrough: passthrough, now: time.Now}
}

// Write captures the line (trimmed of its trailing newline) as an info-level
// record and forwards the original bytes to the passthrough writer.
func (w *Writer) Write(p []byte) (int, error) {
	if line := strings.TrimRight(string(p), "\n"); line != "" {
		w.buf.Append(Record{Time: w.now(), Level: "INFO", Message: line})
	}
	return w.passthrough.Write(p)
}
