// SPDX-License-Identifier: AGPL-3.0-or-later

// Package opsched runs Araldo's periodic background tasks: refreshing
// channel health, reclaiming lost leases, pruning old rows (ADR 0011).
//
// It follows caseline's scheduler (its ADR 0019): every task has a row in
// the database, and a worker runs a task only after leasing that row, so
// any number of workers can run and each task still runs once at a time.
// A worker that dies loses its lease when it expires.
package opsched

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"
)

// Task is a unit of periodic work.
type Task struct {
	// Name identifies the task: short, dotted, stable ("events.prune").
	Name string
	// Interval is the time from one run's end to the next.
	Interval time.Duration
	// Timeout bounds a run (default: the interval, at least one minute).
	Timeout time.Duration
	// Run does the work and reports how much it did. It must be safe to run
	// again after a crash.
	Run func(ctx context.Context) (affected int, err error)
}

func (t Task) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return max(t.Interval, time.Minute)
}

// TaskDef is what the store keeps for a new task.
type TaskDef struct {
	Name     string
	Enabled  bool
	Interval time.Duration
}

// TaskState is a task's schedule as stored.
type TaskState struct {
	Enabled    bool
	Interval   time.Duration
	NextRunAt  time.Time
	LeaseUntil *time.Time
	Failures   int
}

// TaskStatus is a task's state for display.
type TaskStatus struct {
	Name      string
	Enabled   bool
	Interval  time.Duration
	NextRunAt time.Time
	Failures  int
	LastError string
	LastRunAt *time.Time
	LastOKAt  *time.Time
}

// Store keeps task leases.
type Store interface {
	EnsureTasks(ctx context.Context, tasks []TaskDef) error
	// ClaimTask leases name to owner if it is enabled, due and not leased;
	// otherwise it returns the task's state and false.
	ClaimTask(ctx context.Context, name, owner string, now, leaseUntil time.Time) (TaskState, bool, error)
	FinishTask(ctx context.Context, name, owner string, ok bool, errMsg string, finished, next time.Time) error
}

// Scheduler runs tasks until its context ends.
type Scheduler struct {
	store Store
	tasks []Task
	log   *slog.Logger
	// Owner names this worker in leases.
	Owner string
	// Tick is the longest a task's goroutine sleeps before looking again.
	Tick time.Duration
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// New returns a scheduler for tasks, which must have unique names,
// positive intervals and a Run.
func New(st Store, log *slog.Logger, tasks ...Task) (*Scheduler, error) {
	seen := map[string]bool{}
	for _, t := range tasks {
		switch {
		case t.Name == "":
			return nil, errors.New("opsched: task without a name")
		case seen[t.Name]:
			return nil, fmt.Errorf("opsched: task %q registered twice", t.Name)
		case t.Interval <= 0:
			return nil, fmt.Errorf("opsched: task %q needs a positive interval", t.Name)
		case t.Run == nil:
			return nil, fmt.Errorf("opsched: task %q has no Run", t.Name)
		}
		seen[t.Name] = true
	}
	host, _ := os.Hostname()
	return &Scheduler{store: st, tasks: tasks, log: log, Owner: host + "/" + strconv.Itoa(os.Getpid()), Tick: 30 * time.Second, Now: time.Now}, nil
}

// Run registers the tasks, then runs each whenever it is due until ctx
// ends, and waits for in-flight runs.
func (s *Scheduler) Run(ctx context.Context) error {
	defs := make([]TaskDef, len(s.tasks))
	for i, t := range s.tasks {
		defs[i] = TaskDef{Name: t.Name, Enabled: true, Interval: t.Interval}
	}
	if err := s.store.EnsureTasks(ctx, defs); err != nil {
		return fmt.Errorf("opsched: register tasks: %w", err)
	}
	var wg sync.WaitGroup
	for _, t := range s.tasks {
		wg.Go(func() { s.loop(ctx, t) })
	}
	wg.Wait()
	return nil
}

func (s *Scheduler) loop(ctx context.Context, t Task) {
	for {
		next, err := s.Attempt(ctx, t)
		if err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "task scheduling failed", "task", t.Name, "err", err)
		}
		wait := s.Tick
		if !next.IsZero() {
			wait = min(max(next.Sub(s.Now()), 0), s.Tick)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait + jitter(wait)):
		}
	}
}

// Attempt runs t if this worker can claim it, and returns when the task is
// next worth looking at (zero if unknown).
func (s *Scheduler) Attempt(ctx context.Context, t Task) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	now := s.Now()
	timeout := t.timeout()
	// A claim must not be cut short by shutdown, or the lease could be
	// taken without this worker knowing.
	claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	state, claimed, err := s.store.ClaimTask(claimCtx, t.Name, s.Owner, now, now.Add(timeout+30*time.Second))
	cancel()
	if err != nil {
		return time.Time{}, err
	}
	if !claimed {
		switch {
		case !state.Enabled:
			return time.Time{}, nil
		case state.LeaseUntil != nil && state.LeaseUntil.After(now):
			return *state.LeaseUntil, nil
		default:
			return state.NextRunAt, nil
		}
	}
	started := s.Now()
	affected, runErr := s.execute(ctx, t, timeout)
	finished := s.Now()
	next := finished.Add(state.Interval)
	ok, msg := true, ""
	switch {
	case runErr != nil && ctx.Err() != nil:
		ok, msg, next = false, "worker stopped", finished // due again at once
	case runErr != nil:
		ok, msg = false, runErr.Error()
		next = finished.Add(Backoff(state.Failures+1, state.Interval))
		s.log.ErrorContext(ctx, "task failed", "task", t.Name, "err", msg, "consecutive_failures", state.Failures+1)
	case affected > 0:
		s.log.InfoContext(ctx, "task ran", "task", t.Name, "affected", affected, "duration", finished.Sub(started).String())
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.store.FinishTask(finishCtx, t.Name, s.Owner, ok, msg, finished, next); err != nil {
		return time.Time{}, fmt.Errorf("record run: %w", err)
	}
	return next, nil
}

// execute calls t.Run under the timeout; a panic is a failure.
func (s *Scheduler) execute(ctx context.Context, t Task, timeout time.Duration) (affected int, err error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	affected, err = t.Run(runCtx)
	if err == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("did not finish within %s", timeout)
	}
	return affected, err
}

// Backoff is the delay after the nth consecutive failure: 30 seconds
// doubling, up to the task's interval (or five minutes for faster tasks).
func Backoff(failures int, interval time.Duration) time.Duration {
	limit := max(interval, 5*time.Minute)
	d := 30 * time.Second
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

func jitter(d time.Duration) time.Duration {
	limit := min(d/10, 5*time.Second)
	if limit <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}
