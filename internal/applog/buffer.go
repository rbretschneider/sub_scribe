// Package applog captures the application's log stream into a bounded in-memory
// ring buffer so the web UI can show recent activity and errors without the user
// needing shell access to the container. It also implements slog.Handler so it
// can sit in front of the normal stdout logger.
package applog

import (
	"sync"
	"time"
)

// defaultCapacity is the number of recent records retained when none is given.
// It is generous because the buffer backs both the global log viewer and the
// per-job log panels, and a single noisy download can produce hundreds of lines.
const defaultCapacity = 5000

// Record is one captured log line, flattened for display: a level, a message
// (with any structured attributes appended), and when it happened.
type Record struct {
	Time    time.Time
	Level   string
	Message string
	// TaskID attributes the line to the queued task that was running when it was
	// logged, or zero for lines outside any task. It is what lets a job's own log
	// be shown on its detail page.
	TaskID int64
}

// Buffer is a fixed-size, concurrency-safe ring of the most recent log records.
type Buffer struct {
	mu       sync.Mutex
	records  []Record
	capacity int
}

// NewBuffer creates a Buffer retaining up to capacity recent records (a
// non-positive capacity uses the default).
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Buffer{records: make([]Record, 0, capacity), capacity: capacity}
}

// Append adds a record, evicting the oldest once the buffer is full.
func (b *Buffer) Append(record Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.records) == b.capacity {
		copy(b.records, b.records[1:])
		b.records[len(b.records)-1] = record
		return
	}
	b.records = append(b.records, record)
}

// Recent returns up to limit of the most recent records, newest first. A
// non-positive limit returns all retained records.
func (b *Buffer) Recent(limit int) []Record {
	b.mu.Lock()
	defer b.mu.Unlock()

	count := len(b.records)
	if limit > 0 && limit < count {
		count = limit
	}
	out := make([]Record, count)
	for i := 0; i < count; i++ {
		out[i] = b.records[len(b.records)-1-i]
	}
	return out
}

// ForTask returns the retained records produced while taskID was running, in the
// order they happened — a job's log reads top to bottom, unlike the newest-first
// global viewer. A non-positive limit returns every retained line for the task.
func (b *Buffer) ForTask(taskID int64, limit int) []Record {
	if taskID == noTaskID {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	matches := make([]Record, 0)
	for _, record := range b.records {
		if record.TaskID == taskID {
			matches = append(matches, record)
		}
	}
	if limit > 0 && limit < len(matches) {
		matches = matches[len(matches)-limit:]
	}
	return matches
}
