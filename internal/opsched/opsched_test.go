// SPDX-License-Identifier: AGPL-3.0-or-later

package opsched

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store with the same lease rules as Postgres.
type memStore struct {
	mu    sync.Mutex
	tasks map[string]*memTask
}

type memTask struct {
	state   TaskState
	owner   string
	lastErr string
}

func newMem() *memStore { return &memStore{tasks: map[string]*memTask{}} }

func (m *memStore) EnsureTasks(_ context.Context, defs []TaskDef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range defs {
		if _, ok := m.tasks[d.Name]; !ok {
			m.tasks[d.Name] = &memTask{state: TaskState{Enabled: d.Enabled, Interval: d.Interval}}
		}
	}
	return nil
}

func (m *memStore) ClaimTask(_ context.Context, name, owner string, now, leaseUntil time.Time) (TaskState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[name]
	st := t.state
	if !st.Enabled || st.NextRunAt.After(now) || (st.LeaseUntil != nil && !st.LeaseUntil.Before(now)) {
		return st, false, nil
	}
	t.state.LeaseUntil, t.owner = &leaseUntil, owner
	return t.state, true, nil
}

func (m *memStore) FinishTask(_ context.Context, name, owner string, ok bool, errMsg string, _, next time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[name]
	if t.owner != owner {
		return errors.New("lost lease")
	}
	t.state.LeaseUntil, t.owner, t.state.NextRunAt, t.lastErr = nil, "", next, errMsg
	if ok {
		t.state.Failures = 0
	} else {
		t.state.Failures++
	}
	return nil
}

func sched(t *testing.T, st Store, tasks ...Task) *Scheduler {
	t.Helper()
	s, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), tasks...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAttemptRunsDueTasksOnce(t *testing.T) {
	t.Parallel()
	st := newMem()
	runs := 0
	task := Task{Name: "a", Interval: time.Minute, Run: func(context.Context) (int, error) { runs++; return 1, nil }}
	s := sched(t, st, task)
	now := time.Unix(1000, 0)
	s.Now = func() time.Time { return now }
	if err := st.EnsureTasks(t.Context(), []TaskDef{{Name: "a", Enabled: true, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	next, err := s.Attempt(t.Context(), task)
	if err != nil || runs != 1 || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("first attempt: runs %d next %s err %v", runs, next, err)
	}
	// Not due yet: nothing runs.
	if _, err := s.Attempt(t.Context(), task); err != nil || runs != 1 {
		t.Fatalf("second attempt ran early: runs %d err %v", runs, err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Attempt(t.Context(), task); err != nil || runs != 2 {
		t.Fatalf("third attempt: runs %d err %v", runs, err)
	}
}

func TestLeaseBlocksAnotherWorker(t *testing.T) {
	t.Parallel()
	st := newMem()
	_ = st.EnsureTasks(t.Context(), []TaskDef{{Name: "a", Enabled: true, Interval: time.Minute}})
	now := time.Unix(1000, 0)
	if _, ok, _ := st.ClaimTask(t.Context(), "a", "other", now, now.Add(time.Hour)); !ok {
		t.Fatal("setup claim failed")
	}
	ran := false
	task := Task{Name: "a", Interval: time.Minute, Run: func(context.Context) (int, error) { ran = true; return 0, nil }}
	s := sched(t, st, task)
	s.Now = func() time.Time { return now }
	next, err := s.Attempt(t.Context(), task)
	if err != nil || ran || !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("ran %v next %s err %v; want to wait for the other worker's lease", ran, next, err)
	}
}

func TestFailuresBackOffAndPanicsAreFailures(t *testing.T) {
	t.Parallel()
	st := newMem()
	_ = st.EnsureTasks(t.Context(), []TaskDef{{Name: "p", Enabled: true, Interval: time.Hour}})
	task := Task{Name: "p", Interval: time.Hour, Run: func(context.Context) (int, error) { panic("boom") }}
	s := sched(t, st, task)
	now := time.Unix(1000, 0)
	s.Now = func() time.Time { return now }
	next, err := s.Attempt(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Sub(now); got != 30*time.Second {
		t.Fatalf("retry after %s, want 30s", got)
	}
	if st.tasks["p"].state.Failures != 1 || st.tasks["p"].lastErr != "panic: boom" {
		t.Fatalf("state %+v err %q", st.tasks["p"].state, st.tasks["p"].lastErr)
	}
}

func TestBackoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		failures int
		interval time.Duration
		want     time.Duration
	}{
		{1, time.Hour, 30 * time.Second},
		{2, time.Hour, time.Minute},
		{3, time.Hour, 2 * time.Minute},
		{20, time.Hour, time.Hour},
		{20, 5 * time.Second, 5 * time.Minute},
	}
	for _, tt := range tests {
		if got := Backoff(tt.failures, tt.interval); got != tt.want {
			t.Errorf("Backoff(%d, %s) = %s, want %s", tt.failures, tt.interval, got, tt.want)
		}
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	run := func(context.Context) (int, error) { return 0, nil }
	bad := [][]Task{
		{{Name: "", Interval: time.Second, Run: run}},
		{{Name: "a", Interval: 0, Run: run}},
		{{Name: "a", Interval: time.Second}},
		{{Name: "a", Interval: time.Second, Run: run}, {Name: "a", Interval: time.Second, Run: run}},
	}
	for _, tasks := range bad {
		if _, err := New(newMem(), slog.Default(), tasks...); err == nil {
			t.Errorf("New(%+v) succeeded", tasks)
		}
	}
}
