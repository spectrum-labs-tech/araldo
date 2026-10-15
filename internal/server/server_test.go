// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

type fakeHealth struct{ ok atomic.Bool }

func (f *fakeHealth) Healthy() bool { return f.ok.Load() }

// router serves every request with "served" and names its route by mux.
type router struct{ mux *http.ServeMux }

func newRouter(patterns ...string) router {
	r := router{mux: http.NewServeMux()}
	for _, p := range patterns {
		r.mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("served")) })
	}
	return r
}

func (r router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

func (r router) Route(req *http.Request) string {
	_, p := r.mux.Handler(req)
	return p
}

func TestDatabaseGuard(t *testing.T) {
	t.Parallel()
	ok := newRouter("/")
	db := &fakeHealth{}
	h := Handler(ok, ok, func(context.Context) error { return nil }, db, slog.New(slog.NewTextHandler(io.Discard, nil)), tracenoop.NewTracerProvider())
	tests := []struct {
		path    string
		healthy bool
		status  int
		body    string
	}{
		{"/v1/posts", true, http.StatusOK, "served"},
		{"/v1/posts", false, http.StatusServiceUnavailable, `"code":"database_unavailable"`},
		{"/v1/posts", false, http.StatusServiceUnavailable, `"request_id":"req_`},
		{"/v1/posts", false, http.StatusServiceUnavailable, `"doc_url":"https://`},
		{"/brands", false, http.StatusServiceUnavailable, "cannot reach its database"},
		{"/static/app.css", false, http.StatusOK, "served"},
		{"/healthz", false, http.StatusOK, "ok"},
	}
	for _, tt := range tests {
		db.ok.Store(tt.healthy)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.body) {
			t.Errorf("GET %s (healthy %v) = %d %q, want %d containing %q", tt.path, tt.healthy, rec.Code, rec.Body.String(), tt.status, tt.body)
		}
		if tt.status == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
			t.Errorf("GET %s: a 503 without Retry-After", tt.path)
		}
	}
}

// TestRequestSpans checks each request is a span named by its route, with
// the method, route and status only: never the path's IDs, the query or a
// header (ADR 0014). Health checks and static files get none.
func TestRequestSpans(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	db := &fakeHealth{}
	db.ok.Store(true)
	api, web := newRouter("GET /v1/posts/{id}"), newRouter("GET /posts", "GET /static/")
	h := Handler(api, web, func(context.Context) error { return nil }, db, slog.New(slog.NewTextHandler(io.Discard, nil)), tp)
	for _, path := range []string{"/v1/posts/post_secretid?api_key=ald_live_secret", "/posts", "/healthz", "/static/app.css", "/nowhere"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer ald_live_secret")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	spans := rec.Ended()
	var names []string
	for _, sp := range spans {
		names = append(names, sp.Name())
		for _, kv := range sp.Attributes() {
			if v := kv.Value.String(); strings.Contains(v, "secret") {
				t.Errorf("span %s carries %s=%s", sp.Name(), kv.Key, v)
			}
		}
	}
	if strings.Join(names, ",") != "GET /v1/posts/{id},GET /posts,GET" {
		t.Fatalf("spans: %v", names)
	}
	got := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	if got["http.request.method"] != "GET" || got["http.route"] != "GET /v1/posts/{id}" || got["http.response.status_code"] != "200" || len(got) != 3 {
		t.Fatalf("the API span's attributes: %v", got)
	}
}

// panicker panics on every request.
type panicker struct{}

func (panicker) ServeHTTP(http.ResponseWriter, *http.Request) { panic("boom") }
func (panicker) Route(*http.Request) string                   { return "" }

// TestPanicsAreAnswered checks a panic is answered: under /v1 as a problem
// with the request's ID, elsewhere as a page.
func TestPanicsAreAnswered(t *testing.T) {
	t.Parallel()
	h := Handler(panicker{}, panicker{}, func(context.Context) error { return nil }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		tracenoop.NewTracerProvider())
	tests := []struct{ path, contentType, body string }{
		{"/v1/posts", "application/problem+json", `"code":"internal_error"`},
		{"/brands", "text/plain; charset=utf-8", "Request req_"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != tt.contentType || !strings.Contains(rec.Body.String(), tt.body) {
			t.Errorf("GET %s = %d %q %q, want 500 %s containing %q", tt.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(),
				tt.contentType, tt.body)
		}
		if tt.contentType == "application/problem+json" && !strings.Contains(rec.Body.String(), `"request_id":"req_`) {
			t.Errorf("GET %s: no request ID in %s", tt.path, rec.Body.String())
		}
	}
}
