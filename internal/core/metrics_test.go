// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/opsched"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

var testNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// fakeSource is the database as the gauges see it.
type fakeSource struct {
	counts []store.TargetCount
	tasks  []opsched.TaskStatus
	err    error
	reads  atomic.Int32
}

func (f *fakeSource) TargetCounts(context.Context, *uuid.UUID, time.Time) ([]store.TargetCount, error) {
	f.reads.Add(1)
	return f.counts, f.err
}

func (f *fakeSource) Tasks(context.Context) ([]opsched.TaskStatus, error) { return f.tasks, f.err }

func testMetrics(t *testing.T, src gaugeSource, tasks ...string) (*metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	m, err := newMetrics(mp, src, nil, tasks, func() time.Time { return testNow }, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return m, reader
}

// point is one data point, flattened: its attributes as "k=v,k=v" sorted.
type point struct {
	attrs string
	value float64
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string][]point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]point{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrString(dp.Attributes), float64(dp.Value)})
				}
			case metricdata.Gauge[int64]:
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrString(dp.Attributes), float64(dp.Value)})
				}
			case metricdata.Gauge[float64]:
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrString(dp.Attributes), dp.Value})
				}
			case metricdata.Histogram[float64]: // the sum of what was recorded
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrString(dp.Attributes), dp.Sum})
				}
			default:
				t.Fatalf("%s: unexpected data %T", m.Name, m.Data)
			}
		}
	}
	return out
}

func attrString(s attribute.Set) string {
	var kv []string
	for _, a := range s.ToSlice() {
		kv = append(kv, string(a.Key)+"="+a.Value.String())
	}
	slices.Sort(kv)
	return strings.Join(kv, ",")
}

func value(points []point, attrs string) (float64, bool) {
	for _, p := range points {
		if p.attrs == attrs {
			return p.value, true
		}
	}
	return 0, false
}

func TestAttemptLabels(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		status  model.TargetStatus
		err     error
		outcome string
		kind    string
	}{
		"published":          {model.TargetPublished, nil, "published", "none"},
		"rate limited":       {model.TargetQueued, &platform.Error{Kind: platform.RateLimited}, "rate_limited", "rate_limited"},
		"transient retry":    {model.TargetQueued, &platform.Error{Kind: platform.Transient}, "retry", "transient"},
		"auth revoked":       {model.TargetQueued, &platform.Error{Kind: platform.AuthRevoked}, "retry", "auth_revoked"},
		"uncertain, held":    {model.TargetNeedsAttention, &platform.Error{Kind: platform.Uncertain}, "needs_attention", "uncertain"},
		"uncertain, retried": {model.TargetQueued, &platform.Error{Kind: platform.Uncertain}, "retry", "uncertain"},
		"rejected":           {model.TargetFailed, &platform.Error{Kind: platform.Rejected}, "failed", "rejected"},
		"past publish_by":    {model.TargetFailed, &platform.Error{Kind: platform.Transient}, "failed", "transient"},
		"wrapped":            {model.TargetQueued, fmt.Errorf("publish: %w", &platform.Error{Kind: platform.Transient}), "retry", "transient"},
		"unclassified":       {model.TargetNeedsAttention, errors.New("connection reset"), "needs_attention", "unknown"},
		"unrecognized kind":  {model.TargetFailed, &platform.Error{Kind: "Something new"}, "failed", "unknown"},
	} {
		kind := errorKind(tt.err)
		if got := attemptOutcome(tt.status, kind); got != tt.outcome || kind != tt.kind {
			t.Errorf("%s: outcome %q kind %q, want %q %q", name, got, kind, tt.outcome, tt.kind)
		}
	}
}

func TestPublishAndDeliveryCounters(t *testing.T) {
	t.Parallel()
	m, reader := testMetrics(t, &fakeSource{})
	ctx := context.Background()
	live := &model.Target{Livemode: true}
	test := &model.Target{}
	m.recordAttempt(ctx, live, platform.Bluesky, true, model.TargetPublished, nil)
	m.recordAttempt(ctx, live, platform.Bluesky, true, model.TargetPublished, nil)
	m.recordAttempt(ctx, live, platform.Mastodon, true, model.TargetNeedsAttention, &platform.Error{Kind: platform.Uncertain})
	m.recordAttempt(ctx, test, platform.Sandbox, true, model.TargetQueued, &platform.Error{Kind: platform.RateLimited})
	m.recordAttempt(ctx, live, "myspace", false, model.TargetFailed, &platform.Error{Kind: platform.Rejected})
	m.recordDelivery(ctx, &store.ClaimedDelivery{Event: model.Event{Livemode: true}}, &store.DeliveryResult{Succeeded: true})
	m.recordDelivery(ctx, &store.ClaimedDelivery{Event: model.Event{Livemode: true}}, &store.DeliveryResult{})
	m.recordDelivery(ctx, &store.ClaimedDelivery{}, &store.DeliveryResult{GiveUp: true})

	got := collect(t, reader)
	for _, tt := range []struct {
		metric string
		attrs  string
		want   float64
	}{
		{"araldo.publish.attempts", "error_kind=none,mode=live,outcome=published,provider=bluesky", 2},
		{"araldo.publish.attempts", "error_kind=uncertain,mode=live,outcome=needs_attention,provider=mastodon", 1},
		{"araldo.publish.attempts", "error_kind=rate_limited,mode=test,outcome=rate_limited,provider=sandbox", 1},
		{"araldo.publish.attempts", "error_kind=rejected,mode=live,outcome=failed,provider=other", 1},
		{"araldo.webhook.delivery.attempts", "mode=live,outcome=succeeded", 1},
		{"araldo.webhook.delivery.attempts", "mode=live,outcome=retry", 1},
		{"araldo.webhook.delivery.attempts", "mode=test,outcome=failed", 1},
	} {
		if v, ok := value(got[tt.metric], tt.attrs); !ok || v != tt.want {
			t.Errorf("%s{%s} = %v (present %v), want %v", tt.metric, tt.attrs, v, ok, tt.want)
		}
	}
	if n := len(got["araldo.publish.attempts"]); n != 4 {
		t.Errorf("%d publish series, want 4: %v", n, got["araldo.publish.attempts"])
	}
}

func TestDatabaseGauges(t *testing.T) {
	t.Parallel()
	oldest := testNow.Add(-90 * time.Second)
	newer := testNow.Add(-10 * time.Second)
	lastOK := time.Unix(1_790_000_000, 0)
	src := &fakeSource{
		counts: []store.TargetCount{
			{Status: model.TargetQueued, Livemode: true, Count: 5, Due: 2, OldestDue: &oldest},
			{Status: model.TargetNeedsAttention, Livemode: true, Count: 1},
			{Status: model.TargetPublished, Livemode: false, Count: 40},
			{Status: model.TargetQueued, Livemode: false, Count: 3, Due: 1, OldestDue: &newer},
		},
		tasks: []opsched.TaskStatus{
			{Name: "publish.reclaim", Enabled: true, LastOKAt: &lastOK, Failures: 0},
			{Name: "events.prune", Enabled: true, Failures: 4}, // never succeeded
			{Name: "sessions.prune", Enabled: false, LastOKAt: &lastOK},
			{Name: "renamed.long_ago", Enabled: true, LastOKAt: &lastOK},
		},
	}
	m, reader := testMetrics(t, src, "publish.reclaim", "events.prune", "sessions.prune")
	got := collect(t, reader)

	targets := got["araldo.post_targets"]
	if len(targets) != len(TargetStatuses)*2 {
		t.Errorf("%d status series, want every status in both modes: %v", len(targets), targets)
	}
	for _, tt := range []struct {
		metric, attrs string
		want          float64
	}{
		{"araldo.post_targets", "mode=live,status=queued", 5},
		{"araldo.post_targets", "mode=live,status=needs_attention", 1},
		{"araldo.post_targets", "mode=live,status=failed", 0},
		{"araldo.post_targets", "mode=test,status=published", 40},
		{"araldo.post_targets", "mode=test,status=canceled", 0},
		{"araldo.post_targets.due", "mode=live", 2},
		{"araldo.post_targets.due", "mode=test", 1},
		{"araldo.post_targets.oldest_due.age", "mode=live", 90},
		{"araldo.post_targets.oldest_due.age", "mode=test", 10},
		{"araldo.task.last_success.timestamp", "task=publish.reclaim", 1_790_000_000},
		{"araldo.task.consecutive_failures", "task=publish.reclaim", 0},
		{"araldo.task.consecutive_failures", "task=events.prune", 4},
	} {
		if v, ok := value(got[tt.metric], tt.attrs); !ok || v != tt.want {
			t.Errorf("%s{%s} = %v (present %v), want %v", tt.metric, tt.attrs, v, ok, tt.want)
		}
	}
	// A task that never succeeded has no timestamp; disabled and
	// unregistered tasks are not reported.
	if n := len(got["araldo.task.last_success.timestamp"]); n != 1 {
		t.Errorf("last success series: %v", got["araldo.task.last_success.timestamp"])
	}
	if n := len(got["araldo.task.consecutive_failures"]); n != 2 {
		t.Errorf("failure streak series: %v", got["araldo.task.consecutive_failures"])
	}

	// Within the cache window, collections reuse one read.
	collect(t, reader)
	if n := src.reads.Load(); n != 1 {
		t.Errorf("%d database reads for two collections, want 1", n)
	}
	m.cache = 0
	collect(t, reader)
	if n := src.reads.Load(); n != 2 {
		t.Errorf("%d database reads without a cache, want 2", n)
	}
}

// Nothing queued anywhere still reports zeros, so the stall alert sees an
// age of 0 rather than a missing series.
func TestDatabaseGaugesWhenEmpty(t *testing.T) {
	t.Parallel()
	_, reader := testMetrics(t, &fakeSource{})
	got := collect(t, reader)
	for _, mode := range []string{"live", "test"} {
		if v, ok := value(got["araldo.post_targets.oldest_due.age"], "mode="+mode); !ok || v != 0 {
			t.Errorf("oldest due age (%s) = %v, %v", mode, v, ok)
		}
		if v, ok := value(got["araldo.post_targets"], "mode="+mode+",status=needs_attention"); !ok || v != 0 {
			t.Errorf("needs_attention (%s) = %v, %v", mode, v, ok)
		}
	}
}

// A database error reports nothing rather than zeros that would hide a
// problem, and does not fail the collection.
func TestDatabaseGaugesOnError(t *testing.T) {
	t.Parallel()
	_, reader := testMetrics(t, &fakeSource{err: errors.New("connection refused")}, "publish.reclaim")
	got := collect(t, reader)
	for _, name := range []string{"araldo.post_targets", "araldo.post_targets.due", "araldo.task.consecutive_failures"} {
		if len(got[name]) != 0 {
			t.Errorf("%s reported on a database error: %v", name, got[name])
		}
	}
}

// scrape records through the Prometheus exporter that autoexport uses for
// OTEL_METRICS_EXPORTER=prometheus and returns what /metrics serves.
func scrape(t *testing.T, src gaugeSource, tasks []string, record func(m *metrics)) (string, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	exp, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		t.Fatal(err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	m, err := newMetrics(mp, src, nil, tasks, func() time.Time { return testNow }, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	record(m)
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String(), reg
}

// The Prometheus names are what the chart's alerts and the docs use; an
// exporter upgrade that renames them must fail here first.
func TestPrometheusNames(t *testing.T) {
	t.Parallel()
	lastOK := testNow.Add(-time.Minute)
	src := &fakeSource{tasks: []opsched.TaskStatus{{Name: "publish.reclaim", Enabled: true, LastOKAt: &lastOK}}}
	_, reg := scrape(t, src, []string{"publish.reclaim"}, func(m *metrics) {
		m.recordAttempt(context.Background(), &model.Target{Livemode: true}, platform.Bluesky, true, model.TargetPublished, nil)
		m.recordDelivery(context.Background(), &store.ClaimedDelivery{}, &store.DeliveryResult{Succeeded: true})
	})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string][]string{}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "araldo_") {
			continue
		}
		var names []string
		for _, l := range f.GetMetric()[0].GetLabel() {
			if !strings.HasPrefix(l.GetName(), "otel_scope_") {
				names = append(names, l.GetName())
			}
		}
		slices.Sort(names)
		labels[f.GetName()] = names
	}
	want := map[string][]string{
		"araldo_publish_attempts_total":              {"error_kind", "mode", "outcome", "provider"},
		"araldo_webhook_delivery_attempts_total":     {"mode", "outcome"},
		"araldo_post_targets":                        {"mode", "status"},
		"araldo_post_targets_due":                    {"mode"},
		"araldo_post_targets_oldest_due_age_seconds": {"mode"},
		"araldo_task_last_success_timestamp_seconds": {"task"},
		"araldo_task_consecutive_failures":           {"task"},
	}
	for name, l := range want {
		if got, ok := labels[name]; !ok || !slices.Equal(got, l) {
			t.Errorf("%s labels = %v (present %v), want %v", name, got, ok, l)
		}
	}
	for name := range labels {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected metric %s", name)
		}
	}
}

// ADR 0014: no token, secret, content, error message or ID reaches the
// exporter, whatever the instrumented code is handed.
func TestMetricsLeakNothing(t *testing.T) {
	t.Parallel()
	orgID, postID, targetID, channelID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	endpointID, eventID, deliveryID := uuid.New(), uuid.New(), uuid.New()
	forbidden := []string{
		"tok_live_5ecr3t", "whsec_s1gn1ng", "Bearer abc.def.ghi", "app-password-hunter2",
		"Unannounced: Project Nightingale ships Friday", "https://hooks.example.com/private-path",
		"secret response body", "the platform said: duplicate status",
		orgID.String(), postID.String(), targetID.String(), channelID.String(),
		endpointID.String(), eventID.String(), deliveryID.String(),
		id.Format(id.Target, targetID), id.Format(id.Post, postID), id.Format(id.Channel, channelID),
		"brand-slug-secret-launch", "Secret Brand", "owner@example.com",
	}
	oldest := testNow.Add(-time.Minute)
	src := &fakeSource{
		counts: []store.TargetCount{{Status: model.TargetQueued, Livemode: true, Count: 1, Due: 1, OldestDue: &oldest}},
		tasks: []opsched.TaskStatus{{Name: "publish.reclaim", Enabled: true, LastOKAt: &oldest, Failures: 1,
			LastError: "dial tcp: password authentication failed for tok_live_5ecr3t"}},
	}
	text, reg := scrape(t, src, []string{"publish.reclaim"}, func(m *metrics) {
		ctx := context.Background()
		target := &model.Target{
			ID: targetID, OrgID: orgID, PostID: postID, ChannelID: channelID, Livemode: true, Provider: platform.Bluesky,
			Parts:        []string{"Unannounced: Project Nightingale ships Friday"},
			ErrorMessage: "the platform said: duplicate status", ChannelName: "Secret Brand", LeaseOwner: "owner@example.com",
		}
		for _, err := range []error{
			&platform.Error{Kind: platform.Uncertain, Code: "brand-slug-secret-launch", Msg: "the platform said: duplicate status " +
				"Unannounced: Project Nightingale ships Friday", Err: errors.New("Bearer abc.def.ghi")},
			fmt.Errorf("app-password-hunter2 rejected for %s", id.Format(id.Channel, channelID)),
			nil,
		} {
			m.recordAttempt(ctx, target, platform.Bluesky, true, model.TargetNeedsAttention, err)
		}
		status := 500
		d := &store.ClaimedDelivery{
			Delivery: model.Delivery{ID: deliveryID, OrgID: orgID, EndpointID: endpointID, EventID: eventID, ResponseBody: "secret response body"},
			Event:    model.Event{ID: eventID, OrgID: orgID, Livemode: true, Type: "post.created", Data: []byte(`{"text":"Unannounced: Project Nightingale ships Friday"}`)},
			Endpoint: model.WebhookEndpoint{ID: endpointID, OrgID: orgID, URL: "https://hooks.example.com/private-path", Secret: []byte("whsec_s1gn1ng")},
		}
		m.recordDelivery(ctx, d, &store.DeliveryResult{ResponseStatus: &status, ResponseBody: "secret response body", Error: "tok_live_5ecr3t"})
	})
	for _, f := range forbidden {
		if strings.Contains(text, f) {
			t.Errorf("the scrape contains %q:\n%s", f, text)
		}
	}
	// Every label is one of the bounded set, with a value from its domain.
	allowed := map[string]func(string) bool{
		"provider": isProvider,
		"mode":     func(v string) bool { return v == "live" || v == "test" },
		"outcome": func(v string) bool {
			return slices.Contains([]string{"published", "retry", "rate_limited", "needs_attention", "failed", "succeeded"}, v)
		},
		"error_kind": func(v string) bool {
			return slices.Contains([]string{"none", "unknown", "rate_limited", "auth_revoked", "rejected", "transient", "uncertain"}, v)
		},
		"status": func(v string) bool {
			return slices.ContainsFunc(TargetStatuses, func(s model.TargetStatus) bool { return string(s) == v })
		},
		"task": func(v string) bool { return v == "publish.reclaim" },
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "araldo_") {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if strings.HasPrefix(l.GetName(), "otel_scope_") {
					continue
				}
				ok := allowed[l.GetName()]
				if ok == nil || !ok(l.GetValue()) {
					t.Errorf("%s has label %s=%q outside the bounded set", f.GetName(), l.GetName(), l.GetValue())
				}
			}
		}
	}
}

func isProvider(v string) bool {
	return v == "other" || slices.Contains([]platform.Provider{platform.Sandbox, platform.Bluesky, platform.Mastodon, platform.Discord,
		platform.Telegram, platform.X, platform.Facebook, platform.Instagram, platform.Threads, platform.LinkedIn}, platform.Provider(v))
}

// TestPublishLateness checks how late each attempt started is recorded by
// provider and mode, a target not yet due counts as on time, and one with
// no due time (held for a slot) is not recorded.
func TestPublishLateness(t *testing.T) {
	t.Parallel()
	m, reader := testMetrics(t, &fakeSource{})
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	m.recordLateness(ctx, &model.Target{Livemode: true, Provider: platform.Bluesky, NextAttemptAt: at(-90 * time.Second)}, now)
	m.recordLateness(ctx, &model.Target{Livemode: true, Provider: platform.Bluesky, NextAttemptAt: at(-10 * time.Second)}, now)
	m.recordLateness(ctx, &model.Target{Provider: platform.Sandbox, NextAttemptAt: at(time.Minute)}, now)
	m.recordLateness(ctx, &model.Target{Livemode: true, Provider: platform.X}, now)
	got := collect(t, reader)["araldo.publish.lateness"]
	want := map[string]float64{"mode=live,provider=bluesky": 100, "mode=test,provider=sandbox": 0}
	if len(got) != len(want) {
		t.Fatalf("lateness series: %+v", got)
	}
	for _, p := range got {
		if w, ok := want[p.attrs]; !ok || p.value != w {
			t.Errorf("%s: %v, want %v", p.attrs, p.value, w)
		}
	}
}
