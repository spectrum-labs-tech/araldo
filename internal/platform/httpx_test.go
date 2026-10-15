// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJSONClassifiesResponses(t *testing.T) {
	t.Parallel()
	// A 5xx to a write may have been carried out (the platform saved the
	// post, then failed), so only a read's is safe to retry.
	tests := []struct {
		method string
		status int
		header map[string]string
		want   Kind
		retry  time.Duration
		safe   bool
	}{
		{http.MethodPost, 429, map[string]string{"Retry-After": "7"}, RateLimited, 7 * time.Second, false},
		{http.MethodPost, 401, nil, AuthRevoked, 0, false},
		{http.MethodPost, 400, nil, Rejected, 0, false},
		{http.MethodPost, 422, nil, Rejected, 0, false},
		{http.MethodPost, 500, nil, Uncertain, 0, false},
		{http.MethodPost, 503, map[string]string{"Retry-After": "3"}, Uncertain, 3 * time.Second, false},
		{http.MethodPost, 502, nil, Uncertain, 0, false},
		{http.MethodPut, 504, nil, Uncertain, 0, false},
		{http.MethodGet, 500, nil, Transient, 0, false},
		{http.MethodGet, 503, map[string]string{"Retry-After": "3"}, Transient, 3 * time.Second, false},
		{http.MethodGet, 502, nil, Transient, 0, false},
		// A preparatory write (an upload) publishes nothing.
		{http.MethodPost, 500, nil, Transient, 0, true},
		{http.MethodPost, 400, nil, Rejected, 0, true},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range tt.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}))
		var in any
		if tt.method != http.MethodGet {
			in = map[string]string{"a": "b"}
		}
		ctx := t.Context()
		if tt.safe {
			ctx = RetrySafe(ctx)
		}
		err := JSON(ctx, srv.Client(), tt.method, srv.URL, nil, in, nil)
		srv.Close()
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != tt.want || pe.RetryAfter != tt.retry || pe.Status != tt.status || string(pe.Body) != `{"error":"nope"}` {
			t.Errorf("%s HTTP %d: err = %v, want kind %s retry %s", tt.method, tt.status, err, tt.want, tt.retry)
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

func TestMultipartStream(t *testing.T) {
	t.Parallel()
	body, ct, length, err := MultipartStream([][2]string{{"chat_id", "1"}, {"caption", "hi"}},
		StreamFile{Field: "video", Name: "v.mp4", Type: "video/mp4", Size: 5, Body: strings.NewReader("12345")})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(body)
	if int64(len(all)) != length {
		t.Fatalf("length %d, read %d", length, len(all))
	}
	_, params, _ := mime.ParseMediaType(ct)
	r := multipart.NewReader(bytes.NewReader(all), params["boundary"])
	form, err := r.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := form.File["video"][0].Open()
	data, _ := io.ReadAll(f)
	if form.Value["chat_id"][0] != "1" || form.Value["caption"][0] != "hi" || string(data) != "12345" ||
		form.File["video"][0].Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("form %+v, file %q", form.Value, data)
	}
}

func TestFetchBoundsAndKeepsErrors(t *testing.T) {
	t.Parallel()
	long := `{"error":{"message":"` + strings.Repeat("x", 500) + `","code":190}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("a", 2000)))
		case "/fail":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(long))
		default:
			if r.Header.Get("User-Agent") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/next")
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(srv.Close)
	tests := []struct {
		name    string
		method  string
		path    string
		max     int64
		safe    bool
		kind    Kind // "" means no error from Fetch
		status  int
		errBody string
	}{
		{"ok", http.MethodPost, "/ok", 0, false, "", 200, ""},
		{"error status keeps the whole body", http.MethodPost, "/fail", 0, false, "", 400, long},
		{"too large write", http.MethodPost, "/big", 1000, false, Uncertain, 0, ""},
		{"too large read", http.MethodGet, "/big", 1000, false, Transient, 0, ""},
		{"too large preparatory write", http.MethodPost, "/big", 1000, true, Transient, 0, ""},
		{"a larger limit", http.MethodGet, "/big", 4000, false, "", 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tt.safe {
				ctx = RetrySafe(ctx)
			}
			resp, err := Fetch(ctx, srv.Client(), Request{Method: tt.method, URL: srv.URL + tt.path, MaxBody: tt.max})
			if tt.kind != "" {
				if KindOf(err) != tt.kind {
					t.Fatalf("err = %v, want %s", err, tt.kind)
				}
				return
			}
			if err != nil || resp.Status != tt.status {
				t.Fatalf("Fetch = %+v, %v", resp, err)
			}
			if tt.errBody == "" {
				if resp.Err() != nil {
					t.Fatalf("Err() = %v", resp.Err())
				}
				return
			}
			var pe *Error
			if !errors.As(resp.Err(), &pe) || pe.Status != tt.status || string(pe.Body) != tt.errBody || HTTPStatus(resp.Err()) != tt.status {
				t.Fatalf("Err() = %+v", resp.Err())
			}
		})
	}
}

func TestRequireID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id   string
		want Kind
	}{{"1", ""}, {"", Uncertain}}
	for _, tt := range tests {
		err := RequireID(tt.id, "the platform")
		if (err == nil) != (tt.want == "") || err != nil && KindOf(err) != tt.want {
			t.Errorf("RequireID(%q) = %v", tt.id, err)
		}
	}
}
