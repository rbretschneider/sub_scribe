package applog

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func rec(msg string) Record { return Record{Time: time.Now(), Level: "INFO", Message: msg} }

func TestBufferRecentNewestFirst(t *testing.T) {
	b := NewBuffer(10)
	b.Append(rec("first"))
	b.Append(rec("second"))
	b.Append(rec("third"))

	got := b.Recent(0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Message != "third" || got[2].Message != "first" {
		t.Errorf("order = %q..%q, want newest first", got[0].Message, got[2].Message)
	}
}

func TestBufferEvictsOldestWhenFull(t *testing.T) {
	b := NewBuffer(2)
	b.Append(rec("a"))
	b.Append(rec("b"))
	b.Append(rec("c"))

	got := b.Recent(0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (capacity)", len(got))
	}
	if got[0].Message != "c" || got[1].Message != "b" {
		t.Errorf("kept %q,%q, want c,b (a evicted)", got[0].Message, got[1].Message)
	}
}

func TestBufferRecentLimit(t *testing.T) {
	b := NewBuffer(10)
	for _, m := range []string{"1", "2", "3", "4"} {
		b.Append(rec(m))
	}
	if got := b.Recent(2); len(got) != 2 || got[0].Message != "4" {
		t.Errorf("Recent(2) = %+v, want the 2 newest", got)
	}
}

func TestHandlerCapturesLevelAndAttrs(t *testing.T) {
	b := NewBuffer(10)
	logger := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), b))

	logger.Error("task failed", "task_id", 7, "error", "boom")

	got := b.Recent(1)
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].Level != "ERROR" {
		t.Errorf("level = %q, want ERROR", got[0].Level)
	}
	if want := "task failed task_id=7 error=boom"; got[0].Message != want {
		t.Errorf("message = %q, want %q", got[0].Message, want)
	}
	_ = context.Background()
}

// discard is an io.Writer that drops everything, for the passthrough handler.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
