//go:build integration

// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// scratchDatabase creates an empty database of the test's own, since a
// test that breaks the schema must not break the shared one, and returns
// its URL.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "araldo_scratch_" + hex.EncodeToString(b)
	admin := connect(t, dsn)
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cleanup := context.WithoutCancel(t.Context())
	t.Cleanup(func() {
		// A scratch database, not rows: dropping it leaves the shared one alone.
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(cleanup)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(strings.Replace(dsn, "pool_max_conns=4", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestMigrate checks `araldo migrate` brings a database to the latest
// version, and fails, rather than reporting success, when the migration
// does.
func TestMigrate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	latest, err := store.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	db := scratchDatabase(t)
	if v, err := app.Migrate(ctx, db); err != nil || v != latest {
		t.Fatalf("Migrate = %d, %v; want %d", v, err, latest)
	}
	// A schema at a version this binary has no migration for cannot be
	// brought up to date.
	c := connect(t, db)
	defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()
	if _, err := c.Exec(ctx, `UPDATE schema_migrations SET version = 999999, dirty = false`); err != nil {
		t.Fatal(err)
	}
	if v, err := app.Migrate(ctx, db); err == nil {
		t.Fatalf("Migrate on a schema it cannot migrate succeeded at version %d", v)
	}
}
