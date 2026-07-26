package applog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Handler is an slog.Handler that records every log entry into a Buffer and then
// forwards it to an inner handler (normally the stdout logger), so logs appear
// both in the container output and in the in-app viewer.
type Handler struct {
	inner slog.Handler
	buf   *Buffer
	attrs []slog.Attr
}

// NewHandler wraps inner so records are also captured into buf.
func NewHandler(inner slog.Handler, buf *Buffer) *Handler {
	return &Handler{inner: inner, buf: buf}
}

// Enabled defers to the inner handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle captures the record into the buffer and forwards it to the inner
// handler. The running task's id is read from the context, so any code logging
// with a *Context slog call is attributed to the job that triggered it without
// having to know a job exists.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	h.buf.Append(Record{
		Time:    record.Time,
		Level:   record.Level.String(),
		Message: h.format(record),
		TaskID:  TaskFromContext(ctx),
	})
	return h.inner.Handle(ctx, record)
}

// WithAttrs returns a handler that includes attrs on future records.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), buf: h.buf, attrs: append(h.mergedAttrs(), attrs...)}
}

// WithGroup defers grouping to the inner handler; the captured message keeps a
// flat key=value rendering, which is what the simple viewer needs.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), buf: h.buf, attrs: h.attrs}
}

// format renders a record's message plus its structured attributes into a single
// human-readable line, so an error's details are visible in the viewer.
func (h *Handler) format(record slog.Record) string {
	var sb strings.Builder
	sb.WriteString(record.Message)
	for _, attr := range h.mergedAttrs() {
		writeAttr(&sb, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(&sb, attr)
		return true
	})
	return sb.String()
}

// mergedAttrs returns a copy of the handler's accumulated attributes.
func (h *Handler) mergedAttrs() []slog.Attr {
	return append([]slog.Attr(nil), h.attrs...)
}

// writeAttr appends " key=value" for a non-empty attribute.
func writeAttr(sb *strings.Builder, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) {
		return
	}
	fmt.Fprintf(sb, " %s=%v", attr.Key, attr.Value.Any())
}
