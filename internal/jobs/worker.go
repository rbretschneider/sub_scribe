package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// defaultWorkers is the worker count when none is configured. Downloads are
// I/O-bound on yt-dlp, so a small pool keeps CPU and network sane while still
// overlapping work.
const defaultWorkers = 2

// defaultPollInterval is how long an idle worker waits before checking the queue
// again when it finds no runnable task.
const defaultPollInterval = 2 * time.Second

// PoolConfig groups the worker pool's tunables so the constructor keeps a small
// signature and new options don't grow the argument list.
type PoolConfig struct {
	Workers      int
	PollInterval time.Duration
	Logger       *slog.Logger
	// TagContext, when set, decorates the context a task runs under. It is how the
	// log capture attributes every line a handler produces to the task that caused
	// it, without this package knowing anything about logging storage.
	TagContext func(ctx context.Context, taskID int64) context.Context
}

// Pool runs a fixed number of workers that claim and execute tasks. It owns no
// task-specific logic; behavior comes entirely from the injected Queue and
// Registry, so it is closed to modification as new task types are added.
type Pool struct {
	queue        Queue
	registry     *Registry
	clock        Clock
	workers      int
	pollInterval time.Duration
	log          *slog.Logger
	tagContext   func(ctx context.Context, taskID int64) context.Context
}

// NewPool constructs a worker pool, applying sensible defaults for any unset
// configuration.
func NewPool(queue Queue, registry *Registry, clock Clock, cfg PoolConfig) *Pool {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.TagContext == nil {
		cfg.TagContext = untaggedContext
	}
	return &Pool{
		queue:        queue,
		registry:     registry,
		clock:        clock,
		workers:      cfg.Workers,
		pollInterval: cfg.PollInterval,
		log:          cfg.Logger,
		tagContext:   cfg.TagContext,
	}
}

// untaggedContext is the default TagContext: it leaves the context alone, so a
// pool built without log attribution behaves exactly as before.
func untaggedContext(ctx context.Context, _ int64) context.Context {
	return ctx
}

// Run starts the workers and blocks until the context is cancelled, at which
// point it waits for in-flight tasks to finish before returning.
func (p *Pool) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go p.workerLoop(ctx, &wg)
	}
	wg.Wait()
}

// workerLoop claims and processes tasks until the context is cancelled, backing
// off by pollInterval whenever the queue is empty.
func (p *Pool) workerLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := p.ProcessOnce(ctx)
		if err != nil {
			p.log.Error("claiming task failed", "error", err)
		}
		if !worked {
			p.waitForNextPoll(ctx)
		}
	}
}

// ProcessOnce claims and runs at most one task, reporting whether it did work. It
// is exported so tests can drive a single deterministic step without goroutines
// or real time.
func (p *Pool) ProcessOnce(ctx context.Context) (bool, error) {
	task, err := p.queue.Claim(ctx, p.clock.Now())
	if err != nil {
		return false, fmt.Errorf("jobs: claim: %w", err)
	}
	if task == nil {
		return false, nil
	}
	p.dispatch(ctx, *task)
	return true, nil
}

// dispatch runs a task's handler and records the outcome on the queue. The
// handler runs under a context tagged with the task id so its log output is
// attributable, and the start and finish of every task are logged so a job's
// detail view reads as a complete story even when the handler itself is quiet.
func (p *Pool) dispatch(ctx context.Context, task Task) {
	taskCtx := p.tagContext(ctx, task.ID)
	p.log.InfoContext(taskCtx, "task started",
		"task_id", task.ID, "type", task.Type, "attempt", task.Attempts, "of", task.MaxAttempts)

	err := p.runHandler(taskCtx, task)
	now := p.clock.Now()
	// A deferral is a decision, not a failure: the task keeps its retry budget
	// and simply becomes eligible later.
	var deferral *Deferral
	if errors.As(err, &deferral) {
		p.log.InfoContext(taskCtx, "task deferred",
			"task_id", task.ID, "type", task.Type,
			"run_after", deferral.RunAfter, "reason", deferral.Reason)
		if deferErr := p.queue.Defer(ctx, task.ID, deferral.RunAfter, now, deferral.Reason); deferErr != nil {
			p.log.ErrorContext(taskCtx, "deferring task failed", "task_id", task.ID, "error", deferErr)
		}
		return
	}
	if err != nil {
		p.log.ErrorContext(taskCtx, "task failed", "task_id", task.ID, "type", task.Type, "error", err)
		if failErr := p.queue.Fail(ctx, task, err.Error(), now); failErr != nil {
			p.log.ErrorContext(taskCtx, "recording task failure failed", "task_id", task.ID, "error", failErr)
		}
		return
	}
	p.log.InfoContext(taskCtx, "task finished", "task_id", task.ID, "type", task.Type)
	if completeErr := p.queue.Complete(ctx, task.ID, now); completeErr != nil {
		p.log.ErrorContext(taskCtx, "recording task success failed", "task_id", task.ID, "error", completeErr)
	}
}

// runHandler resolves and invokes the handler for a task, converting a panic into
// an error so one misbehaving handler cannot crash a worker or lose the task.
func (p *Pool) runHandler(ctx context.Context, task Task) (err error) {
	handler, err := p.registry.handlerFor(task.Type)
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("jobs: handler for %q panicked: %v", task.Type, recovered)
		}
	}()
	return handler.Handle(ctx, task)
}

// waitForNextPoll sleeps for the poll interval or until the context is cancelled,
// whichever comes first, so shutdown is prompt.
func (p *Pool) waitForNextPoll(ctx context.Context) {
	timer := time.NewTimer(p.pollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
