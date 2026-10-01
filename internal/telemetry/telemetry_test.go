// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/spectrum-labs-tech/araldo/internal/telemetry"
)

func TestMetricsEnabled(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"nothing configured":    {nil, false},
		"prometheus":            {map[string]string{"OTEL_METRICS_EXPORTER": "prometheus"}, true},
		"otlp exporter":         {map[string]string{"OTEL_METRICS_EXPORTER": "otlp"}, true},
		"none":                  {map[string]string{"OTEL_METRICS_EXPORTER": "none"}, false},
		"none despite endpoint": {map[string]string{"OTEL_METRICS_EXPORTER": "none", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}, false},
		"endpoint":              {map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}, true},
		"metrics endpoint":      {map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://collector:4318/v1/metrics"}, true},
		"traces only":           {map[string]string{"OTEL_TRACES_EXPORTER": "otlp"}, false},
		"disabled wins":         {map[string]string{"OTEL_METRICS_EXPORTER": "prometheus", "OTEL_SDK_DISABLED": " TRUE "}, false},
		"disabled not 'true'":   {map[string]string{"OTEL_METRICS_EXPORTER": "prometheus", "OTEL_SDK_DISABLED": "false"}, true},
		"service name alone":    {map[string]string{"OTEL_SERVICE_NAME": "araldo"}, false},
	} {
		if got := telemetry.MetricsEnabled(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("%s: MetricsEnabled = %v, want %v", name, got, tt.want)
		}
	}
}

var discard = slog.New(slog.DiscardHandler)

// Not parallel: t.Setenv.
func TestStartIsANoOpUnlessConfigured(t *testing.T) {
	for _, k := range []string{"OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_SDK_DISABLED"} {
		t.Setenv(k, "")
	}
	tel, err := telemetry.Start(t.Context(), discard, "araldo", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tel.MeterProvider().(noop.MeterProvider); !ok {
		t.Fatalf("meter provider %T, want the no-op one", tel.MeterProvider())
	}
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// OTEL_METRICS_EXPORTER=prometheus serves instruments on its own port
// (loopback here), apart from the public listener, until Shutdown.
// Not parallel: t.Setenv.
func TestStartServesPrometheus(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_METRICS_EXPORTER", "prometheus")
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_HOST", "127.0.0.1")
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", port)
	t.Setenv("OTEL_SERVICE_NAME", "araldo-test")

	tel, err := telemetry.Start(t.Context(), discard, "araldo", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	c, err := tel.MeterProvider().Meter(telemetry.Scope).Int64Counter("araldo.example")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(t.Context(), 3)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	text := string(body)
	if !strings.Contains(text, "araldo_example_total{") || !strings.Contains(text, `service_name="araldo-test"`) ||
		!strings.Contains(text, `service_version="1.2.3"`) {
		t.Fatalf("scrape:\n%s", text)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Get("http://127.0.0.1:" + port + "/metrics"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the scrape endpoint outlived Shutdown")
	}
}
