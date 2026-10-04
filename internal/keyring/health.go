// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// HealthMonitor checks the master keys on an interval and logs when they
// become unavailable or recover. It turns nothing away: operations that
// need a key fail on their own with ErrUnavailable, and everything else
// keeps working.
type HealthMonitor struct {
	keys     *Keyring
	log      *slog.Logger
	interval time.Duration

	mu  sync.Mutex
	err error
	ok  bool
}

// NewHealthMonitor returns a monitor checking k every interval (default 30s).
func NewHealthMonitor(k *Keyring, log *slog.Logger, interval time.Duration) *HealthMonitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &HealthMonitor{keys: k, log: log, interval: interval, ok: true}
}

// Status is the last check's result: nil when the keys are usable.
func (m *HealthMonitor) Status() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Start checks now and then every interval until ctx ends. It returns at
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

// check runs one Check, logging only when the state changes.
func (m *HealthMonitor) check(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := m.keys.Check(cctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err != nil && m.ok:
		m.log.WarnContext(ctx, "master keys unavailable; stored credentials cannot be used until they recover", "err", err)
	case err == nil && !m.ok:
		m.log.InfoContext(ctx, "master keys recovered")
	}
	m.err, m.ok = err, err == nil
}
