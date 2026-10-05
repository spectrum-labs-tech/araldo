//go:build integration

// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"
)

// scratchStore opens a new, empty database of its own, since these tests
// break the schema on purpose and the shared one serves the other tests.
func scratchStore(ctx context.Context, t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "araldo_scratch_" + hex.EncodeToString(b)
	if _, err := admin.pool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	// The test's context has ended by cleanup time.
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		st.Close()
		// A scratch database, not rows: dropping it leaves the shared one alone.
		if a, err := Open(cleanupCtx, dsn); err == nil {
			_, _ = a.pool.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			a.Close()
		}
	})
	return st
}

// TestMigrateRetriesAFailedMigration checks that a migration that failed,
// leaving the schema dirty, is run again by the next migrate, as a retried
// deploy would (ADR 0029).
func TestMigrateRetriesAFailedMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	latest, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	clean := func(st *Store) {
		t.Helper()
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if v, dirty, err := st.SchemaVersion(); err != nil || v != latest || dirty {
			t.Fatalf("after migrating: version %d, dirty %t, %v (want %d, clean)", v, dirty, err, latest)
		}
	}

	t.Run("a migration in one transaction", func(t *testing.T) {
		t.Parallel()
		st := scratchStore(ctx, t)
		m, err := st.migrator()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Migrate(latest - 1); err != nil {
			t.Fatal(err)
		}
		_, _ = m.Close()
		// As if the last migration had failed: its transaction rolled back,
		// and golang-migrate left the schema dirty at its version.
		if _, err := st.pool.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = true`, latest); err != nil {
			t.Fatal(err)
		}
		clean(st)
	})

	t.Run("an index built concurrently", func(t *testing.T) {
		t.Parallel()
		st := scratchStore(ctx, t)
		m, err := st.migrator()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Migrate(14); err != nil {
			t.Fatal(err)
		}
		_, _ = m.Close()
		// A failed CREATE INDEX CONCURRENTLY leaves an invalid index, and
		// the schema dirty at its migration.
		for _, q := range []string{
			`CREATE UNIQUE INDEX channels_mode_key ON channels (org_id, id, livemode)`,
			`UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'channels_mode_key'::regclass`,
			`UPDATE schema_migrations SET version = 15, dirty = true`,
		} {
			if _, err := st.pool.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		clean(st)
		var valid bool
		if err := st.pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'channels_mode_key'::regclass`).Scan(&valid); err != nil || !valid {
			t.Fatalf("the index after retrying: valid %t, %v", valid, err)
		}
	})
}

// TestConcurrentMigrations checks that processes migrating one empty
// database at once all finish. A CREATE INDEX CONCURRENTLY waits for every
// open query, so a migrator blocked in a lock wait would hang it forever,
// as several pods starting together, or test packages, would.
func TestConcurrentMigrations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	first := scratchStore(ctx, t)
	second, err := Open(ctx, first.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	latest, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	errs := make(chan error, 3)
	for _, st := range []*Store{first, second, first} {
		go func() { errs <- st.Migrate(ctx) }()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatalf("a migration: %v", err)
		}
	}
	if v, dirty, err := first.SchemaVersion(); err != nil || v != latest || dirty {
		t.Fatalf("after migrating at once: version %d, dirty %t, %v", v, dirty, err)
	}
}
