package applog

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// newTaskLogger builds a logger that captures into a fresh buffer, discarding the
// passthrough output tests do not care about.
func newTaskLogger() (*slog.Logger, *Buffer) {
	buf := NewBuffer(0)
	handler := NewHandler(slog.NewTextHandler(io.Discard, nil), buf)
	return slog.New(handler), buf
}

func TestLogsAreAttributedToTheRunningTask(t *testing.T) {
	logger, buf := newTaskLogger()

	logger.InfoContext(ContextWithTask(context.Background(), 42), "downloading")
	logger.InfoContext(ContextWithTask(context.Background(), 7), "indexing")
	logger.Info("unrelated startup line")

	forTask := buf.ForTask(42, 0)
	if len(forTask) != 1 {
		t.Fatalf("ForTask(42) returned %d records, want 1: %+v", len(forTask), forTask)
	}
	if forTask[0].Message != "downloading" {
		t.Errorf("Message = %q, want %q", forTask[0].Message, "downloading")
	}
}

func TestForTaskReturnsNothingForUnknownOrZeroTask(t *testing.T) {
	logger, buf := newTaskLogger()
	logger.InfoContext(ContextWithTask(context.Background(), 1), "tagged")
	logger.Info("untagged")

	if got := buf.ForTask(999, 0); len(got) != 0 {
		t.Errorf("ForTask(999) = %+v, want none", got)
	}
	if got := buf.ForTask(0, 0); len(got) != 0 {
		t.Errorf("ForTask(0) = %+v, want none (untagged lines belong to no job)", got)
	}
}

func TestForTaskReadsOldestFirstAndHonoursTheLimit(t *testing.T) {
	logger, buf := newTaskLogger()
	ctx := ContextWithTask(context.Background(), 5)
	for _, message := range []string{"first", "second", "third"} {
		logger.InfoContext(ctx, message)
	}

	all := buf.ForTask(5, 0)
	if len(all) != 3 || all[0].Message != "first" || all[2].Message != "third" {
		t.Fatalf("ForTask returned %+v, want first..third in order", all)
	}

	// A limit keeps the most recent lines, which is what a tail view wants.
	tail := buf.ForTask(5, 2)
	if len(tail) != 2 || tail[0].Message != "second" || tail[1].Message != "third" {
		t.Fatalf("ForTask(limit 2) = %+v, want the last two lines", tail)
	}
}

func TestTaskFromContextDefaultsToNoTask(t *testing.T) {
	if got := TaskFromContext(context.Background()); got != noTaskID {
		t.Errorf("TaskFromContext(plain) = %d, want %d", got, noTaskID)
	}
	//nolint:staticcheck // deliberately checking the nil-context guard
	if got := TaskFromContext(nil); got != noTaskID {
		t.Errorf("TaskFromContext(nil) = %d, want %d", got, noTaskID)
	}
}
