// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Notify signals channel to everyone listening on it (Postgres NOTIFY).
func (s *Store) Notify(ctx context.Context, channel string) error {
	_, err := s.pool.Exec(ctx, `SELECT pg_notify($1, '')`, channel)
	return err
}

// Listen holds a connection listening on channel and calls notified for
// each notification, until ctx ends or the connection fails; it returns
// why. The caller listens again after an error.
func (s *Store) Listen(ctx context.Context, channel string, notified func()) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return err
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		notified()
	}
}

// WithLock runs fn holding the Postgres advisory lock named key, so others
// asking for the same key wait their turn, without a transaction: fn may
// wait on the network with no row locked. fn's store runs on the
// connection that holds the lock, one statement at a time. The lock ends
// with fn, or with the connection if the process dies.
func (s *Store) WithLock(ctx context.Context, key string, fn func(st *Store) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key); err != nil {
		return mapErr(err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
			// Closed, the connection lets the lock go; the pool drops it.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
	}()
	return fn(&Store{pool: s.pool, q: conn})
}
