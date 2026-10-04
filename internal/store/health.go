// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// HealthMonitor tracks whether Postgres answers, so the server can turn
// requests away with a quick 503 while it is down instead of failing each
// one mid-query, and come back on its own when it returns. It pings on an
// interval and keeps the result in a flag that is free to read. One slow
// ping (a cold pool opening its first connection) is not an outage: it takes
// downFailures in a row. Recovery is the pool's: it reconnects on demand,
// and the next good ping clears the flag.
type HealthMonitor struct {
	store    *Store
	log      *slog.Logger
	interval time.Duration
	healthy  atomic.Bool
	failures int // consecutive failed pings; only the monitor's goroutine touches it
}

const (
	// pingTimeout allows for opening a connection, TLS included, through a
	// proxy.
	pingTimeout = 5 * time.Second
	// downFailures failed pings in a row mark the database down.
	downFailures = 2
)

// NewHealthMonitor returns a monitor pinging st every interval (default 3s).
// It starts healthy, so a database that is up costs no 503s before the
// first ping; one that is down fails its queries as Unavailable meanwhile.
func NewHealthMonitor(st *Store, log *slog.Logger, interval time.Duration) *HealthMonitor {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	m := &HealthMonitor{store: st, log: log, interval: interval}
	m.healthy.Store(true)
	return m
}

// Healthy reports the last ping's result.
func (m *HealthMonitor) Healthy() bool { return m.healthy.Load() }

// Start pings now and then every interval until ctx ends. It returns at
// once.
func (m *HealthMonitor) Start(ctx context.Context) {
	go func() {
		m.check(ctx)
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.check(ctx)
			}
		}
	}()
}

// check pings once, logging only when the state changes.
func (m *HealthMonitor) check(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	err := m.store.Ping(pctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		m.failures = 0
		if !m.healthy.Swap(true) {
			m.log.InfoContext(ctx, "database recovered")
		}
		return
	}
	m.failures++
	if m.failures >= downFailures && m.healthy.Swap(false) {
		m.log.WarnContext(ctx, "database unavailable; serving 503 until it recovers", "err", err)
	}
}
