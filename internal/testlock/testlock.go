// SPDX-License-Identifier: AGPL-3.0-or-later

// Package testlock serializes integration tests in different packages that
// would otherwise take each other's work.
package testlock

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Publishing holds a Postgres advisory lock until the test ends. A
// publishing round claims every due target in the database, whatever its
// org, and `go test ./...` runs packages as separate processes at once, so a
// round in one package's test can take the post another package's test is
// about to publish and check. Tests that create posts or publish take this
// before doing either. A test that already holds it (one that sets up two
// orgs, say) carries on.
func Publishing(t testing.TB, dsn string) {
	t.Helper()
	mu.Lock()
	if held[t] {
		mu.Unlock()
		return
	}
	held[t] = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(held, t)
		mu.Unlock()
	})
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("testlock: %v", err)
	}
	// Closing the connection releases the lock.
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('araldo-test-publishing'))`); err != nil {
		t.Fatalf("testlock: %v", err)
	}
}

var (
	mu   sync.Mutex
	held = map[testing.TB]bool{}
)
