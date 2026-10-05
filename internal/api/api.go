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
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
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
	// Query lists each route's query parameters, for the contract test.
	Query map[string][]string
	// open are the routes that need no API key.
	open map[string]bool
	// operatorOnly are the operator API's routes, for operator keys only
	// (ADR 0031).
	operatorOnly map[string]bool
}

// New returns the API handler.
func New(svc *core.Service, log *slog.Logger) *Handler {
	h := &Handler{svc: svc, log: log, mux: http.NewServeMux(), limiter: newLimiter(25, 100), Query: map[string][]string{}, open: map[string]bool{},
		operatorOnly: map[string]bool{}}
	h.routes()
	h.operatorRoutes()
	return h
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// handle registers a route and the query parameters it takes; any other
// query parameter is refused, as unknown body fields are (ADR 0019). A
// parameter named "metadata" also allows metadata[key].
func (h *Handler) handle(pattern string, fn handlerFunc, query ...string) {
	h.Routes = append(h.Routes, pattern)
	h.Query[pattern] = query
	h.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := checkQuery(r, query); err != nil {
			h.fail(w, r, err)
			return
		}
		if err := fn(w, r); err != nil {
			h.fail(w, r, err)
		}
	})
}

// pageQuery are the cursor pagination parameters (ADR 0005).
var pageQuery = []string{"limit", "starting_after", "ending_before"}

func paged(extra ...string) []string { return append(slices.Clone(pageQuery), extra...) }

func checkQuery(r *http.Request, allowed []string) error {
	for k := range r.URL.Query() {
		if slices.Contains(allowed, k) {
			continue
		}
		if name, ok := strings.CutPrefix(k, "metadata["); ok && strings.HasSuffix(name, "]") && slices.Contains(allowed, "metadata") {
			continue
		}
		if len(allowed) == 0 {
			return badRequest("parameter_unknown", k, "Unknown query parameter %q: this operation takes none.", k)
		}
		return badRequest("parameter_unknown", k, "Unknown query parameter %q. This operation takes: %s.", k, strings.Join(allowed, ", "))
	}
	return nil
}

// public registers a route that needs no API key, with the query
// parameters it takes.
func (h *Handler) public(pattern string, fn handlerFunc, query ...string) {
	h.handle(pattern, fn, query...)
	h.open[pattern] = true
}

// ServeHTTP authenticates, rate limits and applies idempotency, then
// routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, pattern := h.mux.Handler(r)
	if pattern == "" {
		h.noRoute(w, r)
		return
	}
	if h.open[pattern] {
		h.mux.ServeHTTP(w, r)
		return
	}
	a, err := h.authenticate(r, h.operatorOnly[pattern])
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="araldo"`)
		h.fail(w, r, err)
		return
	}
	limit, remaining, reset, allowed := h.limiter.take(rateKey(a), time.Now())
	w.Header().Set("RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("RateLimit-Reset", strconv.Itoa(reset))
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(reset, 1)))
		h.fail(w, r, &apperr.Error{Kind: apperr.KindRateLimited, Code: "rate_limited", Message: "Too many requests; slow down."})
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
	if r.Method == http.MethodPost && !a.Operator {
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			h.idempotent(w, r, a, key)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

// noRoute answers a request no route matches as a problem, like every other
// error: 405 with Allow when the path takes other methods, else 404.
func (h *Handler) noRoute(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, p := h.mux.Handler(probe); p != "" {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		h.fail(w, r, &apperr.Error{Kind: apperr.KindMethodNotAllowed, Code: "method_not_allowed",
			Message: fmt.Sprintf("%s %s is not supported; use %s.", r.Method, r.URL.Path, strings.Join(allow, " or "))})
		return
	}
	h.fail(w, r, &apperr.Error{Kind: apperr.KindNotFound, Code: "route_unknown",
		Message: fmt.Sprintf("There is no %s; see /v1/openapi.yaml for the routes.", r.URL.Path)})
}

// authenticate reads the request's credential: on the operator API an
// operator key, anywhere else an API key or user token.
func (h *Handler) authenticate(r *http.Request, operator bool) (core.Actor, error) {
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
	token = strings.TrimSpace(token)
	if operator {
		return h.svc.AuthenticateOperatorKey(r.Context(), token, RequestID(r.Context()))
	}
	if strings.HasPrefix(token, authn.OperatorKeyPrefix) {
		return core.Actor{}, &apperr.Error{Kind: apperr.KindUnauthorized, Code: "api_key_invalid",
			Message: "An operator key works only on the operator API, under /v1/operator/; use an API key or user token here."}
	}
	if strings.HasPrefix(token, authn.UserTokenPrefix) {
		// A person, through the CLI (ADR 0028): the org is the request's.
		return h.svc.AuthenticateUserToken(r.Context(), token, r.Header.Get("Araldo-Org"), RequestID(r.Context()))
	}
	return h.svc.AuthenticateKey(r.Context(), token, RequestID(r.Context()))
}

// rateKey is the credential a request is counted against.
func rateKey(a core.Actor) string {
	if a.OperatorKeyID != nil {
		return "operator_key:" + a.OperatorKeyID.String()
	}
	if a.TokenID != nil {
		return "user_token:" + a.TokenID.String()
	}
	return a.KeyID.String()
}

// idempotent runs a POST at most once per Idempotency-Key (ADR 0005).
func (h *Handler) idempotent(w http.ResponseWriter, r *http.Request, a core.Actor, key string) {
	// A streamed upload (up to a video's size) is not held in memory to
	// fingerprint, so its key covers the route alone: a retry with the key
	// replays the first upload's answer, whatever file it carries.
	var body []byte
	if !streamedUpload(r) {
		var err error
		if body, err = readBody(r, bodyLimit(r)); err != nil {
			h.fail(w, r, err)
			return
		}
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
	if body != nil {
		r.Body = readCloser{bytes.NewReader(body)}
	}
	finished := false
	defer h.releaseIfPanicked(r.Context(), a, key, &finished)
	h.mux.ServeHTTP(rec, r)
	finished = true
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

// releaseIfPanicked releases key when the handler panicked (finished is
// still false), as for an error, so a retry is not refused for a day. The
// panic goes on to the server's recovery.
func (h *Handler) releaseIfPanicked(ctx context.Context, a core.Actor, key string, finished *bool) {
	if *finished {
		return
	}
	if err := h.svc.FinishIdempotent(ctx, a, key, http.StatusInternalServerError, nil); err != nil {
		h.log.ErrorContext(ctx, "releasing idempotency key", "err", err)
	}
}

// Unwrap lets http.ResponseController reach the connection (an upload
// extends its deadlines).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

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
