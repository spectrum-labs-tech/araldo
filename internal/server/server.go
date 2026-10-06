// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server puts the API and the dashboard behind one HTTP handler
// with request IDs, access logs, traces, panic recovery, security headers
// and health checks.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/telemetry"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// Readiness reports whether the server can take traffic.
type Readiness func(ctx context.Context) error

// Health is the database's last known state (store.HealthMonitor).
type Health interface{ Healthy() bool }

// Router is a handler that can name the route a request matches.
type Router interface {
	http.Handler
	Route(r *http.Request) string
}

// Handler routes /v1/ to the API, health checks, and everything else to
// the dashboard. While db is unhealthy, everything but the health checks
// and static files gets a 503 at once. Each request is a span from tp
// (ADR 0014), named by its route, never its path or query.
func Handler(apiH, webH Router, ready Readiness, db Health, log *slog.Logger, tp trace.TracerProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			log.WarnContext(ctx, "not ready", "err", err)
			http.Error(w, "not ready\n", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/v1/", dbGuard(db, apiH, true))
	mux.Handle("/", dbGuard(db, webH, false))
	route := func(r *http.Request) string {
		switch {
		case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
			return r.Method + " " + r.URL.Path
		case strings.HasPrefix(r.URL.Path, "/v1/"):
			return apiH.Route(r)
		}
		return webH.Route(r)
	}
	return middleware(mux, log, tp.Tracer(telemetry.Scope), route)
}

// dbGuard answers 503 while the database is down, as an API problem or a
// plain page, so a Postgres outage is one quick refusal per request rather
// than a failure partway through each. Static files need no database.
func dbGuard(db Health, next http.Handler, isAPI bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if db == nil || db.Healthy() || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "30")
		w.Header().Set("Cache-Control", "no-store")
		if isAPI {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Service Unavailable","status":503,` +
				`"code":"database_unavailable","detail":"The database is unavailable; try again shortly."}` + "\n"))
			return
		}
		http.Error(w, "Araldo cannot reach its database right now. Try again in a minute.", http.StatusServiceUnavailable)
	})
}

// endSpan records the response status on a request's span.
func endSpan(span trace.Span, sw *statusWriter) {
	if sw.status == 0 {
		return
	}
	span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
	if sw.status >= 500 {
		span.SetStatus(codes.Error, http.StatusText(sw.status))
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

// Unwrap lets http.ResponseController reach the connection.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(s int) {
	w.status = s
	w.ResponseWriter.WriteHeader(s)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush lets streaming responses through.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// traced reports whether a request gets a span: not health checks or
// static files, which would drown the rest.
func traced(path string) bool {
	return path != "/healthz" && path != "/readyz" && !strings.HasPrefix(path, "/static/")
}

func middleware(next http.Handler, log *slog.Logger, tracer trace.Tracer, route func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		if traced(r.URL.Path) {
			// A new trace per request: a traceparent header from the
			// internet is not trusted to choose sampling. The attributes
			// are the method, the route pattern and the status: never the
			// path (it holds IDs), the query, headers or bodies.
			name := route(r)
			if name == "" {
				name = r.Method
			}
			ctx, span := tracer.Start(r.Context(), name, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", name)))
			defer span.End()
			r = r.WithContext(ctx)
			defer func() { endSpan(span, sw) }()
		}
		rid := id.Make(id.Request)
		w.Header().Set("Request-Id", rid)
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// The dashboard sets its own Content-Security-Policy, with a nonce.
		if r.Method == http.MethodPost && !strings.HasPrefix(r.URL.Path, "/v1/") {
			limit := int64(1 << 20)
			if r.URL.Path == "/posts" {
				limit = web.MaxPostForm // it can carry images
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		r = r.WithContext(api.WithRequestID(r.Context(), rid))
		defer func() { //nolint:contextcheck // logs with the request's own context
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler { //nolint:errorlint // net/http's sentinel panic value
					panic(p)
				}
				log.ErrorContext(r.Context(), "panic serving request", "panic", p, "path", r.URL.Path, "request_id", rid)
				if sw.status == 0 {
					http.Error(sw, "internal error", http.StatusInternalServerError)
				}
			}
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || strings.HasPrefix(r.URL.Path, "/static/") {
				return
			}
			// Paths only: query strings can hold filters and tokens.
			log.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds(), "request_id", rid)
		}()
		next.ServeHTTP(sw, r)
	})
}

// Serve runs h on addr until ctx ends, then drains for up to 20 seconds.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr: addr, Handler: h,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errc := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
