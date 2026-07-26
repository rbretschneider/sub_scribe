package jobs

import (
	"context"
	"fmt"
)

// Handler executes one task of a particular type. Implementations do the real
// work (indexing, downloading, feed generation) and return an error to trigger
// the queue's retry/backoff policy.
type Handler interface {
	Handle(ctx context.Context, task Task) error
}

// HandlerFunc adapts a plain function to the Handler interface.
type HandlerFunc func(ctx context.Context, task Task) error

// Handle calls the underlying function.
func (f HandlerFunc) Handle(ctx context.Context, task Task) error {
	return f(ctx, task)
}

// Registry maps task types to their handlers. Registering a handler for a new
// type is how work is added, so the dispatcher never grows a type switch
// (Open-Closed).
type Registry struct {
	handlers map[TaskType]Handler
}

// NewRegistry creates an empty handler registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[TaskType]Handler)}
}

// Register associates a handler with a task type. Registering the same type twice
// is a programming error and panics at startup rather than silently overwriting.
func (r *Registry) Register(taskType TaskType, handler Handler) {
	if handler == nil {
		panic(fmt.Sprintf("jobs: nil handler for task type %q", taskType))
	}
	if _, exists := r.handlers[taskType]; exists {
		panic(fmt.Sprintf("jobs: duplicate handler for task type %q", taskType))
	}
	r.handlers[taskType] = handler
}

// handlerFor returns the handler for a task type, or an error if none is
// registered, so an unknown task fails loudly instead of being lost.
func (r *Registry) handlerFor(taskType TaskType) (Handler, error) {
	handler, ok := r.handlers[taskType]
	if !ok {
		return nil, fmt.Errorf("no handler registered for task type %q", taskType)
	}
	return handler, nil
}
