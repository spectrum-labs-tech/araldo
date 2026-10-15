//go:build integration

// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// testStore opens the shared test database, migrated. Tests on it create
// their own rows and assert only on those (CLAUDE.md).
func testStore(ctx context.Context, t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// newUser creates a user with a random email.
func newUser(ctx context.Context, t *testing.T, st *Store) *model.User {
	t.Helper()
	u := &model.User{ID: id.New(), Email: "store-" + uuid.NewString() + "@araldo.example", Name: "Store test"}
	if err := st.CreateUser(ctx, u, u.Email); err != nil {
		t.Fatal(err)
	}
	return u
}

// newSession creates a session for u expiring at expires.
func newSession(ctx context.Context, t *testing.T, st *Store, u *model.User, expires time.Time) uuid.UUID {
	t.Helper()
	ss := &model.Session{ID: id.New(), UserID: u.ID, CSRFToken: "csrf", State: "active", ExpiresAt: expires}
	if err := st.CreateSession(ctx, ss, []byte(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	return ss.ID
}

func sessionExists(ctx context.Context, t *testing.T, st *Store, sid uuid.UUID) bool {
	t.Helper()
	var ok bool
	if err := st.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, sid).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestInBatches checks that a batched delete runs until nothing is left,
// whatever the batch size, and touches nothing else.
func TestInBatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := testStore(ctx, t)
	now := time.Now()
	for _, tt := range []struct {
		name    string
		expired int
		batch   int
	}{
		{"several batches", 5, 2},
		{"exactly full batches", 4, 2},
		{"one batch", 3, 10},
		{"nothing to do", 0, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := newUser(ctx, t, st)
			var expired []uuid.UUID
			for range tt.expired {
				expired = append(expired, newSession(ctx, t, st, u, now.Add(-time.Hour)))
			}
			live := newSession(ctx, t, st, u, now.Add(time.Hour))
			n, err := st.inBatches(ctx, tt.batch, `DELETE FROM sessions WHERE id IN (
				SELECT id FROM sessions WHERE user_id = $2 AND expires_at <= $3 LIMIT $1)`, u.ID, now)
			if err != nil || n != tt.expired {
				t.Fatalf("inBatches = %d, %v; want %d", n, err, tt.expired)
			}
			for _, sid := range expired {
				if sessionExists(ctx, t, st, sid) {
					t.Errorf("expired session %s survived", sid)
				}
			}
			if !sessionExists(ctx, t, st, live) {
				t.Error("the live session was deleted")
			}
		})
	}
}

// TestPruneSessions checks the session prune removes expired sessions only.
func TestPruneSessions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := testStore(ctx, t)
	now := time.Now()
	u := newUser(ctx, t, st)
	old := newSession(ctx, t, st, u, now.Add(-time.Minute))
	live := newSession(ctx, t, st, u, now.Add(time.Hour))
	if _, err := st.PruneSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if sessionExists(ctx, t, st, old) {
		t.Error("an expired session survived the prune")
	}
	if !sessionExists(ctx, t, st, live) {
		t.Error("the prune deleted a live session")
	}
}
