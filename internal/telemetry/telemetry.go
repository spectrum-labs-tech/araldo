// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry sets up OpenTelemetry metrics (ADR 0014).
//
// Configuration is the standard OTEL_* environment variables, read by the
// OpenTelemetry SDK: OTEL_METRICS_EXPORTER ("prometheus" serves a scrape
// endpoint on OTEL_EXPORTER_PROMETHEUS_HOST:OTEL_EXPORTER_PROMETHEUS_PORT,
// default localhost:9464; "otlp" pushes to OTEL_EXPORTER_OTLP_ENDPOINT;
// "console"; "none"), OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES,
// OTEL_SDK_DISABLED and the rest of the specification. Nothing is exported
// until a metrics exporter or an OTLP endpoint is set.
//
// The instruments themselves live with the code they measure (core,
// opsched) and take their MeterProvider explicitly. Their attributes are
// bounded and never carry tokens, content, error messages or tenant IDs.
package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Scope is the instrumentation scope of Araldo's own instruments.
const Scope = "github.com/spectrum-labs-tech/araldo"

// MetricsEnabled reports whether metric export is configured: a metrics
// exporter other than "none", or an OTLP endpoint, and OTEL_SDK_DISABLED
// not "true". Unconfigured, the SDK would push to an OTLP collector on
// localhost and log an error on every export.
func MetricsEnabled(getenv func(string) string) bool {
	if strings.EqualFold(strings.TrimSpace(getenv("OTEL_SDK_DISABLED")), "true") {
		return false
	}
	if e := strings.TrimSpace(getenv("OTEL_METRICS_EXPORTER")); e != "" {
		return e != "none"
	}
	return getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != ""
}

// Telemetry is the running SDK.
type Telemetry struct {
	meters   metric.MeterProvider
	shutdown func(context.Context) error
}

// Start builds the MeterProvider the environment asks for, or a no-op one
// when metrics are not configured. service names this binary (overridden
// by OTEL_SERVICE_NAME). The SDK's own errors (an unreachable collector)
// go to log.
func Start(ctx context.Context, log *slog.Logger, service, version string) (*Telemetry, error) {
	t := &Telemetry{meters: noop.NewMeterProvider()}
	if !MetricsEnabled(os.Getenv) {
		return t, nil
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("telemetry export failed", "err", err)
	}))
	// Later options win, so OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES
	// override the defaults.
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(service), semconv.ServiceVersion(version)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, err
	}
	if autoexport.IsNoneMetricReader(reader) {
		return t, nil
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	t.meters, t.shutdown = mp, mp.Shutdown
	return t, nil
}

// MeterProvider is where instruments are created; a no-op when metrics are
// off.
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.meters }

// Shutdown flushes and stops the exporter (and its scrape endpoint). Call
// it with a deadline.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t.shutdown == nil {
		return nil
	}
	err := t.shutdown(ctx)
	t.shutdown = nil
	return err
}
