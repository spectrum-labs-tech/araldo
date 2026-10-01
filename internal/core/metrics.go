// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/opsched"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/telemetry"
)

// Metrics (ADR 0014). Attributes are bounded: provider, mode, status,
// outcome, error kind, task name. Never an org, brand, channel, post or
// target ID, never content, never an error message.

// TargetStatuses are the post target statuses the status gauge reports,
// zero included, so a series never vanishes when its count drops to zero.
var TargetStatuses = []model.TargetStatus{
	model.TargetQueued, model.TargetPublishing, model.TargetHeld, model.TargetNeedsAttention,
	model.TargetFailed, model.TargetPublished, model.TargetCanceled,
}

// Attribute keys.
const (
	attrMode      = attribute.Key("mode")
	attrStatus    = attribute.Key("status")
	attrProvider  = attribute.Key("provider")
	attrOutcome   = attribute.Key("outcome")
	attrErrorKind = attribute.Key("error_kind")
	attrTask      = attribute.Key("task")
)

// gaugeCache is how long one reading of the database serves collections,
// so several scrapers cost one query.
const gaugeCache = 10 * time.Second

// gaugeSource is what the database gauges read; *store.Store is one.
type gaugeSource interface {
	TargetCounts(ctx context.Context, orgID *uuid.UUID, now time.Time) ([]store.TargetCount, error)
	Tasks(ctx context.Context) ([]opsched.TaskStatus, error)
}

// metrics holds the instruments. The zero value is unusable; newMetrics
// with a no-op provider is the "off" state.
type metrics struct {
	attempts   metric.Int64Counter
	deliveries metric.Int64Counter

	src   gaugeSource
	org   *uuid.UUID // nil: every org; tests narrow it to their own
	tasks []string   // registered task names: stale rows are not reported
	now   func() time.Time
	log   *slog.Logger
	cache time.Duration

	mu      sync.Mutex
	readAt  time.Time
	counts  []store.TargetCount
	taskRow []opsched.TaskStatus
}

func newMetrics(mp metric.MeterProvider, src gaugeSource, org *uuid.UUID, tasks []string, now func() time.Time, log *slog.Logger) (*metrics, error) {
	meter := mp.Meter(telemetry.Scope)
	m := &metrics{src: src, org: org, tasks: tasks, now: now, log: log, cache: gaugeCache}
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	var err error
	m.attempts, err = meter.Int64Counter("araldo.publish.attempts", metric.WithUnit("{attempt}"),
		metric.WithDescription("Publish attempts by provider, mode, what the publisher did (outcome) and the platform's error kind."))
	add(err)
	m.deliveries, err = meter.Int64Counter("araldo.webhook.delivery.attempts", metric.WithUnit("{attempt}"),
		metric.WithDescription("Webhook delivery attempts by mode and outcome (succeeded, retry, failed: gave up)."))
	add(err)
	targets, err := meter.Int64ObservableGauge("araldo.post_targets", metric.WithUnit("{target}"),
		metric.WithDescription("Post targets by status and mode, across every org."))
	add(err)
	due, err := meter.Int64ObservableGauge("araldo.post_targets.due", metric.WithUnit("{target}"),
		metric.WithDescription("Queued targets the publisher could claim now (due, channel active and not rate limited), by mode."))
	add(err)
	oldest, err := meter.Float64ObservableGauge("araldo.post_targets.oldest_due.age", metric.WithUnit("s"),
		metric.WithDescription("How long the oldest claimable target has been due, by mode; 0 when none is due."))
	add(err)
	lastOK, err := meter.Float64ObservableGauge("araldo.task.last_success.timestamp", metric.WithUnit("s"),
		metric.WithDescription("Unix time of each background task's last successful run, from the database."))
	add(err)
	streak, err := meter.Int64ObservableGauge("araldo.task.consecutive_failures", metric.WithUnit("{failure}"),
		metric.WithDescription("Failures of each background task since its last success, from the database."))
	add(err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, tasks, ok := m.read(ctx)
		if !ok {
			return nil // nothing observed: the series go stale rather than lie
		}
		m.observeTargets(o, targets, due, oldest, counts)
		m.observeTasks(o, lastOK, streak, tasks)
		return nil
	}, targets, due, oldest, lastOK, streak)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// noopMetrics records nothing.
func noopMetrics() *metrics {
	m, err := newMetrics(noop.NewMeterProvider(), nil, nil, nil, time.Now, slog.New(slog.DiscardHandler))
	if err != nil {
		panic(err) // the no-op provider never fails
	}
	return m
}

// read returns the database state, cached for m.cache.
func (m *metrics) read(ctx context.Context) ([]store.TargetCount, []opsched.TaskStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.counts != nil && now.Sub(m.readAt) < m.cache {
		return m.counts, m.taskRow, true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	counts, err := m.src.TargetCounts(ctx, m.org, now)
	if err != nil {
		m.log.WarnContext(ctx, "reading queue metrics failed", "err", err)
		return nil, nil, false
	}
	tasks, err := m.src.Tasks(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "reading task metrics failed", "err", err)
		return nil, nil, false
	}
	if counts == nil {
		counts = []store.TargetCount{}
	}
	m.counts, m.taskRow, m.readAt = counts, tasks, now
	return counts, tasks, true
}

func (m *metrics) observeTargets(o metric.Observer, targets, due metric.Int64Observable, oldest metric.Float64Observable, counts []store.TargetCount) {
	now := m.now()
	for _, live := range []bool{true, false} {
		mode := attrMode.String(modeName(live))
		var nDue int
		var oldestAt *time.Time
		for _, status := range TargetStatuses {
			var n int
			for _, c := range counts {
				if c.Status == status && c.Livemode == live {
					n += c.Count
					nDue += c.Due
					if c.OldestDue != nil && (oldestAt == nil || c.OldestDue.Before(*oldestAt)) {
						oldestAt = c.OldestDue
					}
				}
			}
			o.ObserveInt64(targets, int64(n), metric.WithAttributes(attrStatus.String(string(status)), mode))
		}
		o.ObserveInt64(due, int64(nDue), metric.WithAttributes(mode))
		age := 0.0
		if oldestAt != nil {
			age = max(now.Sub(*oldestAt).Seconds(), 0)
		}
		o.ObserveFloat64(oldest, age, metric.WithAttributes(mode))
	}
}

func (m *metrics) observeTasks(o metric.Observer, lastOK metric.Float64Observable, streak metric.Int64Observable, rows []opsched.TaskStatus) {
	for _, name := range m.tasks {
		for _, t := range rows {
			if t.Name != name || !t.Enabled {
				continue
			}
			task := metric.WithAttributes(attrTask.String(name))
			if t.LastOKAt != nil {
				o.ObserveFloat64(lastOK, float64(t.LastOKAt.UnixNano())/1e9, task)
			}
			o.ObserveInt64(streak, int64(t.Failures), task)
		}
	}
}

// recordAttempt counts one publish attempt. status is where the target
// went; pubErr is what the adapter returned. Only bounded labels are
// derived from them: the provider when it has an adapter, the mode, the
// outcome and the error's kind, never its message.
func (m *metrics) recordAttempt(ctx context.Context, t *model.Target, provider platform.Provider, known bool, status model.TargetStatus, pubErr error) {
	kind := errorKind(pubErr)
	if !known {
		provider = "other"
	}
	m.attempts.Add(ctx, 1, metric.WithAttributes(
		attrProvider.String(string(provider)),
		attrMode.String(modeName(t.Livemode)),
		attrOutcome.String(attemptOutcome(status, kind)),
		attrErrorKind.String(kind),
	))
}

// recordDelivery counts one webhook delivery attempt.
func (m *metrics) recordDelivery(ctx context.Context, d *store.ClaimedDelivery, res *store.DeliveryResult) {
	outcome := "retry"
	switch {
	case res.Succeeded:
		outcome = "succeeded"
	case res.GiveUp:
		outcome = "failed"
	}
	m.deliveries.Add(ctx, 1, metric.WithAttributes(attrMode.String(modeName(d.Event.Livemode)), attrOutcome.String(outcome)))
}

func modeName(live bool) string {
	if live {
		return "live"
	}
	return "test"
}

// errorKind is a publish error's classification: "none" for success,
// a platform.Kind, or "unknown" for an unclassified error or an
// unrecognized kind.
func errorKind(err error) string {
	if err == nil {
		return "none"
	}
	var pe *platform.Error
	if !errors.As(err, &pe) {
		return "unknown"
	}
	switch pe.Kind {
	case platform.RateLimited, platform.AuthRevoked, platform.Rejected, platform.Transient, platform.Uncertain:
		return string(pe.Kind)
	}
	return "unknown"
}

// attemptOutcome is what the publisher did with the target: published,
// retry (queued again), rate_limited (queued after the platform's limit),
// needs_attention (maybe posted; a person decides) or failed.
func attemptOutcome(status model.TargetStatus, kind string) string {
	switch status {
	case model.TargetPublished:
		return "published"
	case model.TargetQueued:
		if kind == string(platform.RateLimited) {
			return "rate_limited"
		}
		return "retry"
	case model.TargetNeedsAttention:
		return "needs_attention"
	}
	return "failed"
}

// Instrument records metrics with mp: publish and delivery counters, and
// gauges read from the database on collection (every org, no org label).
// Call it once, before the worker starts.
func (s *Service) Instrument(mp metric.MeterProvider) error {
	return s.instrument(mp, nil)
}

func (s *Service) instrument(mp metric.MeterProvider, org *uuid.UUID) error {
	var names []string
	for _, t := range s.Tasks() {
		names = append(names, t.Name)
	}
	m, err := newMetrics(mp, s.store, org, names, func() time.Time { return s.Now() }, s.log)
	if err != nil {
		return err
	}
	s.metrics = m
	return nil
}
