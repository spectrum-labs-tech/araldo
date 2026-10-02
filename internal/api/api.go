// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api is Araldo's HTTP API (ADR 0005): /v1, authenticated with API
// keys, JSON in and out, errors as problem details, idempotent POSTs. The
// contract is api/openapi.yaml; a test keeps the routes and the contract
// in step.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	actorKey
)

// WithRequestID stores the request's ID.
func WithRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, requestIDKey, rid)
}

// RequestID returns the request's ID ("req_…").
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

func actor(r *http.Request) core.Actor {
	a, _ := r.Context().Value(actorKey).(core.Actor)
	return a
}

// Handler serves /v1.
type Handler struct {
	svc     *core.Service
	log     *slog.Logger
	mux     *http.ServeMux
	limiter *limiter
	// Routes lists "METHOD /path" for every route, for the contract test.
	Routes []string
}

// New returns the API handler.
func New(svc *core.Service, log *slog.Logger) *Handler {
	h := &Handler{svc: svc, log: log, mux: http.NewServeMux(), limiter: newLimiter(25, 100)}
	h.routes()
	return h
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (h *Handler) handle(pattern string, fn handlerFunc) {
	h.Routes = append(h.Routes, pattern)
	h.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			h.fail(w, r, err)
		}
	})
}

// public registers a route that needs no API key.
func (h *Handler) public(pattern string, fn http.HandlerFunc) {
	h.Routes = append(h.Routes, pattern)
	h.mux.HandleFunc(pattern, fn)
}

// ServeHTTP authenticates, rate limits and applies idempotency, then
// routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/openapi.yaml" {
		h.mux.ServeHTTP(w, r)
		return
	}
	a, err := h.authenticate(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="araldo"`)
		h.fail(w, r, err)
		return
	}
	limit, remaining, reset, allowed := h.limiter.take(a.KeyID.String(), time.Now())
	w.Header().Set("RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("RateLimit-Reset", strconv.Itoa(reset))
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(reset, 1)))
		h.fail(w, r, &apperr.Error{Kind: apperr.KindRateLimited, Code: "rate_limited", Message: "Too many requests; slow down."})
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
	if r.Method == http.MethodPost {
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			h.idempotent(w, r, a, key)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) authenticate(r *http.Request) (core.Actor, error) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		// Basic auth with the key as the username, as Stripe allows.
		if user, _, basic := r.BasicAuth(); basic {
			token, ok = user, true
		}
	}
	if !ok || token == "" {
		return core.Actor{}, &apperr.Error{Kind: apperr.KindUnauthorized, Code: "api_key_missing",
			Message: "Send your API key as a bearer token: Authorization: Bearer ald_test_…"}
	}
	return h.svc.AuthenticateKey(r.Context(), strings.TrimSpace(token), RequestID(r.Context()))
}

// idempotent runs a POST at most once per Idempotency-Key (ADR 0005).
func (h *Handler) idempotent(w http.ResponseWriter, r *http.Request, a core.Actor, key string) {
	body, err := readBody(r, bodyLimit(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
	replay, err := h.svc.BeginIdempotent(r.Context(), a, key, sum[:])
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if replay != nil {
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(replay.Status)
		_, _ = w.Write(replay.Body) //nolint:gosec // G705: a stored JSON API response, served as application/json
		return
	}
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	r.Body = readCloser{bytes.NewReader(body)}
	h.mux.ServeHTTP(rec, r)
	if err := h.svc.FinishIdempotent(r.Context(), a, key, rec.status, rec.buf.Bytes()); err != nil {
		h.log.ErrorContext(r.Context(), "storing idempotent response", "err", err)
	}
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

func readBody(r *http.Request, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, limit+1))
	if err != nil || n > limit {
		return nil, badRequest("body_too_large", "", "Request bodies are limited to %d MiB.", limit>>20)
	}
	return buf.Bytes(), nil
}

// recorder copies the response so it can be stored for replays.
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// limiter is a per-key token bucket held in memory. With several replicas
// each enforces its own share, which is fine for abuse protection.
type limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, buckets: map[string]*bucket{}}
}

func (l *limiter) take(key string, now time.Time) (limit, remaining, reset int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) > 10_000 {
			clear(l.buckets)
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		ok = true
	}
	reset = int(math.Ceil((l.burst - b.tokens) / l.rate))
	return int(l.burst), int(b.tokens), reset, ok
}

// ids parse path values.
func pathID(r *http.Request, p id.Prefix, what string) (uuid.UUID, error) {
	return core.ParseID(p, r.PathValue("id"), what)
}
