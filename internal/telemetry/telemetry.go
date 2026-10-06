// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry sets up OpenTelemetry metrics and traces (ADR 0014).
//
// Configuration is the standard OTEL_* environment variables, read by the
// OpenTelemetry SDK. Traces: OTEL_TRACES_EXPORTER ("otlp", "console",
// "none") or an OTLP endpoint, sampled as OTEL_TRACES_SAMPLER says
// (default: every trace). Metrics: OTEL_METRICS_EXPORTER ("prometheus" serves a scrape
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
	"errors"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Scope is the instrumentation scope of Araldo's own instruments.
const Scope = "github.com/spectrum-labs-tech/araldo"

// TracesEnabled reports whether trace export is configured: a traces
// exporter other than "none", or an OTLP endpoint, and OTEL_SDK_DISABLED
// not "true".
func TracesEnabled(getenv func(string) string) bool {
	if strings.EqualFold(strings.TrimSpace(getenv("OTEL_SDK_DISABLED")), "true") {
		return false
	}
	if e := strings.TrimSpace(getenv("OTEL_TRACES_EXPORTER")); e != "" {
		return e != "none"
	}
	return getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

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
	tracers  trace.TracerProvider
	shutdown []func(context.Context) error
}

// Start builds the MeterProvider and TracerProvider the environment asks
// for, each a no-op when not configured. service names this binary
// (overridden by OTEL_SERVICE_NAME). The SDK's own errors (an unreachable
// collector) go to log.
func Start(ctx context.Context, log *slog.Logger, service, version string) (*Telemetry, error) {
	t := &Telemetry{meters: noop.NewMeterProvider(), tracers: tracenoop.NewTracerProvider()}
	metrics, traces := MetricsEnabled(os.Getenv), TracesEnabled(os.Getenv)
	if !metrics && !traces {
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
	if metrics {
		reader, err := autoexport.NewMetricReader(ctx)
		if err != nil {
			return nil, err
		}
		if !autoexport.IsNoneMetricReader(reader) {
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
			t.meters, t.shutdown = mp, append(t.shutdown, mp.Shutdown)
		}
	}
	if traces {
		exp, err := autoexport.NewSpanExporter(ctx)
		if err != nil {
			_ = t.Shutdown(ctx)
			return nil, err
		}
		if !autoexport.IsNoneSpanExporter(exp) {
			// The sampler comes from OTEL_TRACES_SAMPLER, which the SDK reads.
			tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
			t.tracers, t.shutdown = tp, append(t.shutdown, tp.Shutdown)
		}
	}
	return t, nil
}

// TracerProvider is where spans are made; a no-op when traces are off.
func (t *Telemetry) TracerProvider() trace.TracerProvider { return t.tracers }

// MeterProvider is where instruments are created; a no-op when metrics are
// off.
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.meters }

// Shutdown flushes and stops the exporter (and its scrape endpoint). Call
// it with a deadline.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		errs = append(errs, f(ctx))
	}
	t.shutdown = nil
	return errors.Join(errs...)
}

// LogHandler adds the trace and span IDs of a record's context, when it
// has a span, to every record h writes, so logs and traces join up.
func LogHandler(h slog.Handler) slog.Handler { return traceHandler{h} }

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}
