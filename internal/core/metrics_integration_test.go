// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// gauges collects reader and returns each int64 gauge's points keyed by
// "mode/status" (or "mode" for gauges without a status).
func gauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			out[m.Name] = map[string]int64{}
			for _, dp := range g.DataPoints {
				mode, _ := dp.Attributes.Value(attribute.Key("mode"))
				key := mode.AsString()
				if status, ok := dp.Attributes.Value(attribute.Key("status")); ok {
					key += "/" + status.AsString()
				}
				out[m.Name][key] = dp.Value
			}
		}
	}
	return out
}

func instrumented(t *testing.T, instrument func(metric.MeterProvider) error) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	if err := instrument(mp); err != nil {
		t.Fatal(err)
	}
	return reader
}

// The status gauge counts the targets this test creates: one waiting for
// its time, one published, one canceled. Narrowed to this test's org, the
// counts are exact; database-wide, they include at least these rows.
func TestTargetStatusGauge(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	scoped := instrumented(t, func(mp metric.MeterProvider) error { return core.InstrumentOrg(w.s, mp, w.org.ID) })

	before := gauges(t, scoped)["araldo.post_targets"]
	if len(before) != len(core.TargetStatuses)*2 {
		t.Fatalf("status series before: %v", before)
	}
	for k, v := range before {
		if v != 0 {
			t.Fatalf("a new org has %s = %d", k, v)
		}
	}

	later := w.s.Now().Add(time.Hour).Format(time.RFC3339)
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "waits"}, PublishAt: later}); err != nil {
		t.Fatal(err)
	}
	now, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "goes out"}, PublishAt: "now"})
	if err != nil {
		t.Fatal(err)
	}
	if p := settle(t, w, now.ID); p.Targets[0].Status != model.TargetPublished {
		t.Fatalf("published target: %+v", p.Targets)
	}
	canceled, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "never mind"}, PublishAt: later})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CancelPost(ctx, w.owner, canceled.ID); err != nil {
		t.Fatal(err)
	}

	got := gauges(t, scoped)
	want := map[string]int64{"test/queued": 1, "test/published": 1, "test/canceled": 1}
	for k, v := range got["araldo.post_targets"] {
		if v != want[k] {
			t.Errorf("araldo.post_targets{%s} = %d, want %d", k, v, want[k])
		}
	}
	// The queued target is not due for an hour.
	if due := got["araldo.post_targets.due"]; due["test"] != 0 || due["live"] != 0 {
		t.Errorf("due = %v, want none", due)
	}

	// Database-wide, as production reports it: other tests' rows come and
	// go, but these three stay where they are.
	global := instrumented(t, w.s.Instrument)
	all := gauges(t, global)["araldo.post_targets"]
	for k := range want {
		if all[k] < 1 {
			t.Errorf("database-wide %s = %d, want at least this test's target", k, all[k])
		}
	}
}
