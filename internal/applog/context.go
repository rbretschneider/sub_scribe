package applog

import "context"

// taskContextKey is the private key under which a running task's id travels on
// the context. A private type keeps it out of reach of other packages.
type taskContextKey struct{}

// noTaskID marks a record that was not produced while running a queued task.
const noTaskID int64 = 0

// ContextWithTask tags ctx with the id of the task being run, so every log line
// emitted while it runs can be attributed to it. The worker pool applies this
// once per claim; nothing below it has to pass a task id around.
func ContextWithTask(ctx context.Context, taskID int64) context.Context {
	return context.WithValue(ctx, taskContextKey{}, taskID)
}

// TaskFromContext returns the task id carried by ctx, or zero when the caller is
// not running inside a task.
func TaskFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return noTaskID
	}
	taskID, ok := ctx.Value(taskContextKey{}).(int64)
	if !ok {
		return noTaskID
	}
	return taskID
}
