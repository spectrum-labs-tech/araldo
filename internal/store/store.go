// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store is Araldo's PostgreSQL storage (ADR 0002): hand-written SQL
// over pgx, and the embedded migrations. Every tenant-owned query takes the
// org it is scoped to (ADR 0004).
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Errors.
var (
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is a unique constraint violation.
	ErrConflict = errors.New("store: conflict")
	// ErrReferenced is a foreign key violation: the row is still in use.
	ErrReferenced = errors.New("store: still referenced")
)

//go:embed migrations/*.sql
var migrations embed.FS

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store runs queries on a pool, or inside one transaction.
type Store struct {
	pool *pgxpool.Pool
	q    querier
	inTx bool
}

// Open returns a store on a Postgres pool. It does not wait for the
// database: the pool connects on first use, so Araldo starts while Postgres
// is down and its queries fail as Unavailable until it is back. Only a
// malformed URL is an error.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse database URL: %w", err)
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		// A database that drops packets fails a connection in seconds,
		// not the operating system's minutes (connect_timeout in the URL
		// overrides it).
		cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	return &Store{pool: pool, q: pool}, nil
}

// Unavailable reports whether err means the database could not be reached
// (rather than that a query was wrong).
func Unavailable(err error) bool {
	var connect *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connect) || errors.As(err, &netErr)
}

// Close closes the pool.
func (s *Store) Close() { s.pool.Close() }

// Pool is the underlying pool (for LISTEN).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate brings the schema up to date. golang-migrate holds an advisory
// lock, so several processes starting at once is safe.
//
// A migration that failed before (the schema is dirty at its version) is
// retried: each runs as one transaction, so it left nothing behind, except a
// CREATE INDEX CONCURRENTLY, which leaves an invalid index that is dropped
// first (ADR 0029). Migrations from several processes take turns.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	defer conn.Release()
	// Wait for the lock by trying again and again, never in one blocking
	// call: a CREATE INDEX CONCURRENTLY under way waits for every open query
	// to end, and a waiting pg_advisory_lock would never end, so the two
	// would wait on each other forever.
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrateLock).Scan(&got); err != nil {
			return fmt.Errorf("store: migrate lock: %w", err)
		}
		if got {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: migrate lock: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrateLock) }()
	m, err := s.migrator()
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if v, dirty, err := m.Version(); err == nil && dirty {
		if err := s.undoFailedMigration(ctx, m, v); err != nil {
			return fmt.Errorf("store: migrate: retrying failed migration %d: %w", v, err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- m.Up() }()
	select {
	case <-ctx.Done():
		m.GracefulStop <- true
		return ctx.Err()
	case err := <-done:
		if err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("store: migrate: %w", err)
		}
		return nil
	}
}

// migrateLock is the advisory lock Migrate holds, so two processes never
// retry the same failed migration at once.
const migrateLock = 0x61726c646f6d67 // "arldomg"

// concurrentIndexRE finds the index a CREATE INDEX CONCURRENTLY builds.
var concurrentIndexRE = regexp.MustCompile(`(?i)\bINDEX CONCURRENTLY (?:IF NOT EXISTS )?(\w+)`)

// undoFailedMigration readies migration v, which failed, to run again: it
// drops the invalid index a failed CREATE INDEX CONCURRENTLY leaves, and
// sets the version back to the one before.
func (s *Store) undoFailedMigration(ctx context.Context, m *migrate.Migrate, v uint) error {
	sql, err := migrationSQL(v)
	if err != nil {
		return err
	}
	if mm := concurrentIndexRE.FindStringSubmatch(sql); mm != nil {
		var invalid bool
		err := s.pool.QueryRow(ctx, `SELECT NOT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, mm[1]).Scan(&invalid)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case invalid:
			if _, err := s.pool.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+pgx.Identifier{mm[1]}.Sanitize()); err != nil {
				return err
			}
		}
	}
	return m.Force(previousVersion(v))
}

// previousVersion is the version before v, or none before the first.
func previousVersion(v uint) int {
	if v <= 1 {
		return -1 // golang-migrate's "no version"
	}
	return int(v) - 1
}

// migrationSQL is migration v's up file.
func migrationSQL(v uint) (string, error) {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return "", err
	}
	prefix := fmt.Sprintf("%06d_", v)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".up.sql") {
			b, err := migrations.ReadFile("migrations/" + e.Name())
			return string(b), err
		}
	}
	return "", fmt.Errorf("no migration %d", v)
}

// SchemaVersion reports the applied migration version and whether a
// migration failed halfway.
func (s *Store) SchemaVersion() (version uint, dirty bool, err error) {
	m, err := s.migrator()
	if err != nil {
		return 0, false, err
	}
	defer func() { _, _ = m.Close() }()
	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

// LatestVersion is the newest migration shipped in this binary.
func LatestVersion() (uint, error) {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return 0, err
	}
	var latest uint
	for _, e := range entries {
		var v uint
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err == nil && v > latest {
			latest = v
		}
	}
	return latest, nil
}

func (s *Store) migrator() (*migrate.Migrate, error) {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDBFromPool(s.pool)
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return nil, fmt.Errorf("store: migrate driver: %w", err)
	}
	return migrate.NewWithInstance("iofs", src, "pgx5", driver)
}

// InTx runs fn in a transaction, committing if it returns nil. Inside a
// transaction already, it just runs fn.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if s.inTx {
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(&Store{pool: s.pool, q: tx, inTx: true}); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// snapshot runs fn, a read made of several statements, in one read-only
// REPEATABLE READ transaction, so every statement sees the same moment:
// otherwise a write committed between them (a target published, its post
// marked published) shows half done. Inside a transaction already, it just
// runs fn.
func (s *Store) snapshot(ctx context.Context, fn func(tx *Store) error) error {
	if s.inTx {
		return fn(s)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	return fn(&Store{pool: s.pool, q: tx, inTx: true})
}

// mapErr turns driver errors into store sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pe.ConstraintName)
	}
	if errors.As(err, &pe) && pe.Code == "23503" {
		return fmt.Errorf("%w: %s", ErrReferenced, pe.ConstraintName)
	}
	return err
}

// ConflictOn reports whether err is a unique violation of constraint name.
func ConflictOn(err error, name string) bool {
	return errors.Is(err, ErrConflict) && strings.HasSuffix(err.Error(), ": "+name)
}

// exec runs a statement that must affect a row.
func (s *Store) execOne(ctx context.Context, sql string, args ...any) error {
	tag, err := s.q.Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Page is a cursor page request (ADR 0005): up to Limit rows, newest
// first, older than StartingAfter, or newer than EndingBefore. IDs are
// UUIDv7, so ID order is creation order.
type Page struct {
	Limit         int
	StartingAfter uuid.UUID
	EndingBefore  uuid.UUID
}

// Limit is the page size, defaulted and capped.
func (p Page) Size() int {
	switch {
	case p.Limit <= 0:
		return 20
	case p.Limit > 100:
		return 100
	}
	return p.Limit
}

// pageClause returns the WHERE fragment, ORDER BY and LIMIT for a page over
// column col, with args numbered from n. The query fetches one extra row to
// tell whether more exist; trimPage finishes the job.
func pageClause(p Page, col string, n int) (where, order string, args []any) {
	switch {
	case p.EndingBefore != uuid.Nil:
		return fmt.Sprintf(" AND %s > $%d", col, n), fmt.Sprintf(" ORDER BY %s ASC LIMIT %d", col, p.Size()+1), []any{p.EndingBefore}
	case p.StartingAfter != uuid.Nil:
		return fmt.Sprintf(" AND %s < $%d", col, n), fmt.Sprintf(" ORDER BY %s DESC LIMIT %d", col, p.Size()+1), []any{p.StartingAfter}
	}
	return "", fmt.Sprintf(" ORDER BY %s DESC LIMIT %d", col, p.Size()+1), nil
}

// trimPage cuts the extra row and restores newest-first order.
func trimPage[T any](p Page, rows []T) ([]T, bool) {
	more := len(rows) > p.Size()
	if more {
		rows = rows[:p.Size()]
	}
	if p.EndingBefore != uuid.Nil {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	return rows, more
}

// SchemaState reads the applied migration version straight from
// golang-migrate's table (cheap enough for readiness probes).
func (s *Store) SchemaState(ctx context.Context) (version uint, dirty bool, err error) {
	var v int64
	err = s.q.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&v, &dirty)
	if err != nil {
		return 0, false, mapErr(err)
	}
	return uint(v), dirty, nil //nolint:gosec // versions are positive
}
