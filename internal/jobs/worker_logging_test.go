package jobs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// dispatchWithLogs runs one task through a pool whose logger keeps every
// level, and returns the captured log text.
func dispatchWithLogs(t *testing.T, handler Handler) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	registry := NewRegistry()
	registry.Register(TaskIndexSource, handler)
	clock := fixedClock{t: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	pool := NewPool(&fakeQueue{pending: indexTask()}, registry, clock, PoolConfig{Workers: 1, Logger: logger})

	if _, err := pool.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	return buf.String()
}

// logLineLevel returns the level= token of the first captured line whose
// msg contains message, or "" when no line matches.
func logLineLevel(logs, message string) string {
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "msg="+message) {
			continue
		}
		for _, field := range strings.Fields(line) {
			if level, ok := strings.CutPrefix(field, "level="); ok {
				return level
			}
		}
	}
	return ""
}

// TestRoutineDeferralsAreDebugLevel pins the log-noise fix: with several
// downloads queued, every pacing slot wakes all of them and re-defers all but
// one, and those claim/defer pairs were most of the log while saying nothing
// the queue row does not already say.
func TestRoutineDeferralsAreDebugLevel(t *testing.T) {
	runAfter := time.Date(2026, 7, 21, 12, 10, 0, 0, time.UTC)
	logs := dispatchWithLogs(t, HandlerFunc(func(context.Context, Task) error {
		return DeferRoutine(runAfter, "waiting its turn")
	}))

	if got := logLineLevel(logs, "\"task started\""); got != "DEBUG" {
		t.Errorf("task started level = %q, want DEBUG", got)
	}
	if got := logLineLevel(logs, "\"task deferred\""); got != "DEBUG" {
		t.Errorf("routine deferral level = %q, want DEBUG", got)
	}
}

// TestNonRoutineDeferralsStayVisible pins the contrast: a deferral that is a
// reaction to a problem (a throttle backoff) still logs at the default level.
func TestNonRoutineDeferralsStayVisible(t *testing.T) {
	runAfter := time.Date(2026, 7, 21, 12, 45, 0, 0, time.UTC)
	logs := dispatchWithLogs(t, HandlerFunc(func(context.Context, Task) error {
		return Defer(runAfter, "provider is rate-limiting us")
	}))

	if got := logLineLevel(logs, "\"task deferred\""); got != "INFO" {
		t.Errorf("non-routine deferral level = %q, want INFO", got)
	}
}

// TestFinishedTasksCarryTheirAttemptCounts pins that demoting the start line
// did not lose the attempt story: the outcome line carries it instead.
func TestFinishedTasksCarryTheirAttemptCounts(t *testing.T) {
	logs := dispatchWithLogs(t, HandlerFunc(func(context.Context, Task) error { return nil }))

	if got := logLineLevel(logs, "\"task finished\""); got != "INFO" {
		t.Errorf("task finished level = %q, want INFO", got)
	}
	if !strings.Contains(logs, "msg=\"task finished\"") || !strings.Contains(logs, "attempt=") {
		t.Errorf("finished line must carry the attempt count, got %q", logs)
	}
}
