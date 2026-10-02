// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server puts the API and the dashboard behind one HTTP handler
// with request IDs, access logs, panic recovery, security headers and
// health checks.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// Readiness reports whether the server can take traffic.
type Readiness func(ctx context.Context) error

// Handler routes /v1/ to the API, health checks, and everything else to
// the dashboard.
func Handler(apiH, webH http.Handler, ready Readiness, log *slog.Logger) http.Handler {
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
	mux.Handle("/v1/", apiH)
	mux.Handle("/", webH)
	return middleware(mux, log)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

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

func middleware(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
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
		sw := &statusWriter{ResponseWriter: w}
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
