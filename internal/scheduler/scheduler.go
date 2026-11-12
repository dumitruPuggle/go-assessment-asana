// Package scheduler runs a job periodically at a fixed interval.
//
// Guarantees:
//   - The job runs once immediately, then on every interval boundary.
//   - Runs never overlap: the next run starts only after the previous returns.
//   - If a run overruns one or more boundaries, the missed runs are skipped
//     rather than fired back to back, so a slow job (e.g. one throttled by the
//     Asana rate limiter) can't snowball into a burst of requests.
//   - Between runs it always sleeps a strictly positive duration, so there is
//     no busy loop, and it returns promptly once its context is cancelled.
//   - A job error or panic is logged and the schedule continues.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Job is the unit of work run on each tick. It should honor ctx.
type Job func(ctx context.Context) error

// Scheduler runs a Job every Interval.
type Scheduler struct {
	name     string
	interval time.Duration
	job      Job
	logger   *slog.Logger

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// Option customizes a Scheduler.
type Option func(*Scheduler)

// WithLogger sets the logger. Interval and timing details are logged at Debug
// level; job failures at Error level. The default discards all output.
func WithLogger(l *slog.Logger) Option {
	return func(s *Scheduler) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithName labels log lines, useful when several schedulers run at once.
func WithName(name string) Option {
	return func(s *Scheduler) { s.name = name }
}

// New creates a Scheduler. interval must be positive; a zero interval would
// otherwise mean "run continuously".
func New(interval time.Duration, job Job, opts ...Option) (*Scheduler, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("scheduler interval must be positive, got %v", interval)
	}
	if job == nil {
		return nil, errors.New("scheduler job must not be nil")
	}
	s := &Scheduler{
		name:     "job",
		interval: interval,
		job:      job,
		logger:   slog.New(slog.DiscardHandler),
		now:      time.Now,
		sleep:    sleepCtx,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Run executes the job immediately and then every interval until ctx is
// cancelled. It returns nil on cancellation; job errors never stop it.
func (s *Scheduler) Run(ctx context.Context) error {
	log := s.logger.With("scheduler", s.name)
	log.Debug("scheduler started", "interval", s.interval)
	defer log.Debug("scheduler stopped")

	next := s.now()
	for run := 1; ; run++ {
		if ctx.Err() != nil {
			return nil
		}

		start := s.now()
		log.Debug("run started", "run", run, "interval", s.interval)
		err := s.runOnce(ctx)
		took := s.now().Sub(start)

		if ctx.Err() != nil {
			// Cancelled mid-run; the error is just the cancellation.
			return nil
		}
		if err != nil {
			log.Error("run failed", "run", run, "took", took, "err", err)
		}

		// Advance to the next boundary. If the run overran, skip every missed
		// boundary in one step (no loop) and land on the first future one.
		next = next.Add(s.interval)
		now := s.now()
		if !next.After(now) {
			missed := now.Sub(next)/s.interval + 1
			next = next.Add(missed * s.interval)
			log.Debug("run overran interval, skipping missed runs",
				"run", run, "took", took, "skipped", int(missed))
		}
		wait := next.Sub(now)
		log.Debug("run finished", "run", run, "took", took, "next_run_in", wait, "interval", s.interval)

		if err := s.sleep(ctx, wait); err != nil {
			return nil
		}
	}
}

// runOnce calls the job, converting a panic into an error so one bad run
// cannot kill the schedule.
func (s *Scheduler) runOnce(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
		}
	}()
	return s.job(ctx)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
