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
)

type fakeHealth struct{ ok atomic.Bool }

func (f *fakeHealth) Healthy() bool { return f.ok.Load() }

func TestDatabaseGuard(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("served")) })
	db := &fakeHealth{}
	h := Handler(ok, ok, func(context.Context) error { return nil }, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		path    string
		healthy bool
		status  int
		body    string
	}{
		{"/v1/posts", true, http.StatusOK, "served"},
		{"/v1/posts", false, http.StatusServiceUnavailable, `"code":"database_unavailable"`},
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
