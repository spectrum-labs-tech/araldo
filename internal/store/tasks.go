// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/opsched"
)

// Background task leases (opsched.Store).

func (s *Store) EnsureTasks(ctx context.Context, tasks []opsched.TaskDef) error {
	for _, t := range tasks {
		if _, err := s.q.Exec(ctx, `INSERT INTO scheduled_tasks (name, enabled, interval_ms) VALUES ($1, $2, $3) ON CONFLICT (name) DO NOTHING`,
			t.Name, t.Enabled, t.Interval.Milliseconds()); err != nil {
			return err
		}
	}
	return nil
}

// ClaimTask leases a task that is enabled, due and not leased.
func (s *Store) ClaimTask(ctx context.Context, name, owner string, now, leaseUntil time.Time) (opsched.TaskState, bool, error) {
	var st opsched.TaskState
	var ms int64
	err := s.q.QueryRow(ctx, `UPDATE scheduled_tasks SET lease_owner = $2, lease_until = $4, last_run_at = $3
		WHERE name = $1 AND enabled AND next_run_at <= $3 AND (lease_until IS NULL OR lease_until < $3)
		RETURNING enabled, interval_ms, next_run_at, lease_until, failures`, name, owner, now, leaseUntil).
		Scan(&st.Enabled, &ms, &st.NextRunAt, &st.LeaseUntil, &st.Failures)
	if err == nil {
		st.Interval = time.Duration(ms) * time.Millisecond
		return st, true, nil
	}
	if err != pgx.ErrNoRows { //nolint:errorlint // pgx returns it unwrapped
		return st, false, err
	}
	err = s.q.QueryRow(ctx, `SELECT enabled, interval_ms, next_run_at, lease_until, failures FROM scheduled_tasks WHERE name = $1`, name).
		Scan(&st.Enabled, &ms, &st.NextRunAt, &st.LeaseUntil, &st.Failures)
	st.Interval = time.Duration(ms) * time.Millisecond
	return st, false, mapErr(err)
}

// FinishTask releases the lease and schedules the next run.
func (s *Store) FinishTask(ctx context.Context, name, owner string, ok bool, errMsg string, finished, next time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE scheduled_tasks SET lease_owner = NULL, lease_until = NULL, next_run_at = $5,
		failures = CASE WHEN $3 THEN 0 ELSE failures + 1 END, last_error = $4,
		last_ok_at = CASE WHEN $3 THEN $6 ELSE last_ok_at END
		WHERE name = $1 AND lease_owner = $2`, name, owner, ok, errMsg, next, finished)
	return err
}

// Tasks lists every task's state, for the dashboard.
func (s *Store) Tasks(ctx context.Context) ([]opsched.TaskStatus, error) {
	rows, err := s.q.Query(ctx, `SELECT name, enabled, interval_ms, next_run_at, failures, last_error, last_run_at, last_ok_at
		FROM scheduled_tasks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (opsched.TaskStatus, error) {
		var t opsched.TaskStatus
		var ms int64
		err := r.Scan(&t.Name, &t.Enabled, &ms, &t.NextRunAt, &t.Failures, &t.LastError, &t.LastRunAt, &t.LastOKAt)
		t.Interval = time.Duration(ms) * time.Millisecond
		return t, err
	})
}
