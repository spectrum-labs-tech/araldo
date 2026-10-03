// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// maxBody bounds how much of a platform response is read.
const maxBody = 1 << 20

// Do sends req and classifies transport failures: a request that failed
// before it was fully written cannot have been received (Transient); one
// that failed afterwards might have been (Uncertain, ADR 0011).
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := client.Do(req) //nolint:gosec // G704: adapters get a client that refuses private addresses (netguard)
	if err == nil {
		return resp, nil
	}
	kind := Transient
	if wrote.Load() {
		kind = Uncertain
	}
	return nil, &Error{Kind: kind, Code: "network", Msg: "request to " + req.URL.Host + " failed", Err: err}
}

// JSON sends a JSON request and decodes a JSON response into out (if not
// nil). A non-2xx response becomes a classified *Error.
func JSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, in, out any) error {
	var body []byte
	contentType := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return &Error{Kind: Rejected, Code: "encode", Err: err}
		}
		body, contentType = b, "application/json"
	}
	return Send(ctx, client, method, url, headers, body, contentType, out)
}

// Send sends body (if not nil) as contentType and decodes a JSON response
// into out (if not nil). A non-2xx response becomes a classified *Error.
func Send(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte, contentType string, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	return SendStream(ctx, client, method, url, headers, r, int64(len(body)), contentType, out)
}

// SendStream is Send with a body read as the request is sent, of length
// bytes, such as a video (ADR 0027).
func SendStream(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body io.Reader, length int64,
	contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return &Error{Kind: Rejected, Code: "request", Err: err}
	}
	if body != nil {
		req.ContentLength = length
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Araldo (+https://github.com/spectrum-labs-tech/araldo)")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := Do(client, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return &Error{Kind: Uncertain, Code: "network", Msg: "reading response", Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Classify(resp, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			// The platform accepted the request; we just cannot read its
			// answer, so whether it took effect is unknown.
			return &Error{Kind: Uncertain, Code: "decode", Msg: "unreadable response", Err: err}
		}
	}
	return nil
}

// StreamFile is a file sent in a multipart request as it is read, such as
// a video (ADR 0027).
type StreamFile struct {
	Field, Name, Type string
	Size              int64
	Body              io.Reader
}

// MultipartStream encodes fields, then one streamed file, as
// multipart/form-data without holding the file: the body reads it as the
// request is sent, and length is exact, so no chunked encoding is needed.
func MultipartStream(fields [][2]string, f StreamFile) (body io.Reader, contentType string, length int64, err error) {
	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	for _, fl := range fields {
		if err := w.WriteField(fl[0], fl[1]); err != nil {
			return nil, "", 0, err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": f.Field, "filename": f.Name}))
	h.Set("Content-Type", f.Type)
	if _, err := w.CreatePart(h); err != nil {
		return nil, "", 0, err
	}
	// What Close would write after the last part.
	tail := "\r\n--" + w.Boundary() + "--\r\n"
	return io.MultiReader(&head, f.Body, strings.NewReader(tail)), w.FormDataContentType(), int64(head.Len()) + f.Size + int64(len(tail)), nil
}

// OpenVideo opens a video for an upload, as a platform error when it
// cannot be read (storage is ours, so trying again may work).
func OpenVideo(ctx context.Context, m Media) (io.ReadCloser, error) {
	if m.Open == nil {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: "the video is not available"}
	}
	rc, err := m.Open(ctx)
	if err != nil {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: "reading the video", Err: err}
	}
	return rc, nil
}

// File is one file in a multipart request.
type File struct {
	Field, Name, Type string
	Data              []byte
}

// Multipart encodes fields, in order, then files as multipart/form-data.
// Each field is a name and a value.
func Multipart(fields [][2]string, files []File) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", err
		}
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": f.Field, "filename": f.Name}))
		h.Set("Content-Type", f.Type)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(f.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// Classify turns an error response into a *Error.
func Classify(resp *http.Response, body []byte) *Error {
	msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &Error{Kind: RateLimited, Code: "rate_limited", Msg: msg, RetryAfter: RetryAfter(resp.Header, time.Now())}
	case resp.StatusCode == http.StatusUnauthorized:
		return &Error{Kind: AuthRevoked, Code: "unauthorized", Msg: msg}
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout:
		// A proxy gave up waiting: the platform may have acted.
		return &Error{Kind: Uncertain, Code: "gateway", Msg: msg}
	case resp.StatusCode >= 500:
		return &Error{Kind: Transient, Code: "server_error", Msg: msg, RetryAfter: RetryAfter(resp.Header, time.Now())}
	default:
		return &Error{Kind: Rejected, Code: "rejected", Msg: msg}
	}
}

// RetryAfter reads a Retry-After header (seconds or an HTTP date), or
// X-RateLimit-Reset / RateLimit-Reset (seconds or a Unix time).
func RetryAfter(h http.Header, now time.Time) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s >= 0 {
			return time.Duration(s) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	for _, name := range []string{"RateLimit-Reset", "X-RateLimit-Reset", "X-Rate-Limit-Reset"} {
		v := h.Get(name)
		if v == "" {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 0 {
			if f > 1e9 { // a Unix time
				if d := time.Unix(int64(f), 0).Sub(now); d > 0 {
					return d
				}
				return 0
			}
			return time.Duration(f * float64(time.Second))
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	return 0
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
