// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJSONClassifiesResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		header map[string]string
		want   Kind
		retry  time.Duration
	}{
		{429, map[string]string{"Retry-After": "7"}, RateLimited, 7 * time.Second},
		{401, nil, AuthRevoked, 0},
		{400, nil, Rejected, 0},
		{422, nil, Rejected, 0},
		{500, nil, Transient, 0},
		{503, map[string]string{"Retry-After": "3"}, Transient, 3 * time.Second},
		{502, nil, Uncertain, 0},
		{504, nil, Uncertain, 0},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range tt.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}))
		err := JSON(t.Context(), srv.Client(), http.MethodPost, srv.URL, nil, map[string]string{"a": "b"}, nil)
		srv.Close()
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != tt.want || pe.RetryAfter != tt.retry {
			t.Errorf("HTTP %d: err = %v, want kind %s retry %s", tt.status, err, tt.want, tt.retry)
		}
	}
}

func TestJSONDecodesSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"42"}`))
	}))
	defer srv.Close()
	var out struct{ ID string }
	if err := JSON(t.Context(), srv.Client(), http.MethodPost, srv.URL, map[string]string{"Authorization": "Bearer t"}, struct{}{}, &out); err != nil || out.ID != "42" {
		t.Fatalf("JSON = %v, id %q", err, out.ID)
	}
}

func TestDoDistinguishesBeforeAndAfterSending(t *testing.T) {
	t.Parallel()
	// Nothing listens: the request is never written.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	err = JSON(t.Context(), &http.Client{Timeout: time.Second}, http.MethodPost, "http://"+addr, nil, struct{}{}, nil)
	if KindOf(err) != Transient {
		t.Errorf("connection refused: kind %s, want transient (%v)", KindOf(err), err)
	}

	// The server reads the request, then hangs until the client times out.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	err = JSON(t.Context(), &http.Client{Timeout: 200 * time.Millisecond}, http.MethodPost, srv.URL, nil, struct{}{}, nil)
	if KindOf(err) != Uncertain {
		t.Errorf("timeout after sending: kind %s, want uncertain (%v)", KindOf(err), err)
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{"Retry-After": {"120"}}, 2 * time.Minute},
		{http.Header{"Retry-After": {now.Add(time.Minute).UTC().Format(http.TimeFormat)}}, time.Minute},
		{http.Header{"X-Ratelimit-Reset": {"1700000030"}}, 30 * time.Second},
		{http.Header{"Ratelimit-Reset": {"5"}}, 5 * time.Second},
		{http.Header{}, 0},
	}
	for _, tt := range tests {
		if got := RetryAfter(tt.h, now); got != tt.want {
			t.Errorf("RetryAfter(%v) = %s, want %s", tt.h, got, tt.want)
		}
	}
}

func TestKindOfUnknownIsUncertain(t *testing.T) {
	t.Parallel()
	if KindOf(errors.New("boom")) != Uncertain {
		t.Fatal("an unclassified error must count as uncertain")
	}
}
