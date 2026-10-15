// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// maxBody bounds how much of a platform response is read, unless a
// Request asks for more.
const maxBody = 1 << 20

// maxErrorBody bounds how much of an error response an *Error keeps.
const maxErrorBody = 8 << 10

type retrySafeKey struct{}

// RetrySafe marks requests made with the returned context as preparatory:
// they publish nothing (an upload, a container to publish later, a read
// sent as a POST), so a failure after sending is Transient rather than
// Uncertain, and the post can be tried again. Never use it for the request
// that makes a post public, nor for any request after that one.
func RetrySafe(ctx context.Context) context.Context {
	return context.WithValue(ctx, retrySafeKey{}, true)
}

// retrySafe reports whether repeating req cannot publish twice: a read, or
// a request marked with RetrySafe.
func retrySafe(req *http.Request) bool {
	if req == nil {
		return false
	}
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return true
	}
	safe, _ := req.Context().Value(retrySafeKey{}).(bool)
	return safe
}

// afterSending is the kind of a failure once req may have been received.
func afterSending(req *http.Request) Kind {
	if retrySafe(req) {
		return Transient
	}
	return Uncertain
}

// Do sends req and classifies transport failures: a request that failed
// before it was fully written cannot have been received (Transient); one
// that failed afterwards might have been (Uncertain, ADR 0011), unless
// repeating it is safe (see RetrySafe).
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
		kind = afterSending(req)
	}
	return nil, &Error{Kind: kind, Code: "network", Msg: "request to " + req.URL.Host + " failed", Err: redactURL(err)}
}

// Request is one call to a platform.
type Request struct {
	Method, URL string
	Header      map[string]string
	// Body, if not nil, is sent as ContentType, Length bytes of it.
	Body        io.Reader
	Length      int64
	ContentType string
	// MaxBody bounds the response read; 0 means 1 MiB. A longer response
	// is an error, never silently cut.
	MaxBody int64
}

// Response is a platform's answer, read in full.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	resp   *http.Response
}

// Err classifies a non-2xx response (see Classify); nil for a 2xx.
func (r *Response) Err() error {
	if r.Status >= 200 && r.Status <= 299 {
		return nil
	}
	return Classify(r.resp, r.Body)
}

// Fetch sends r with Araldo's User-Agent and reads the whole response,
// whatever its status. An error is a *Error: a transport failure as Do
// classifies it, or a response that cannot be read or is longer than
// MaxBody (Uncertain once sent, unless repeating the request is safe).
func Fetch(ctx context.Context, client *http.Client, r Request) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, r.Body)
	if err != nil {
		return nil, &Error{Kind: Rejected, Code: "request", Err: redactURL(err)}
	}
	if r.Body != nil {
		req.ContentLength = r.Length
	}
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Araldo (+https://github.com/spectrum-labs-tech/araldo)")
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	resp, err := Do(client, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	limit := r.MaxBody
	if limit <= 0 {
		limit = maxBody
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, &Error{Kind: afterSending(req), Code: "network", Msg: "reading the response from " + req.URL.Host, Err: err}
	}
	if int64(len(raw)) > limit {
		return nil, &Error{Kind: afterSending(req), Code: "response_too_large", Status: resp.StatusCode,
			Msg: fmt.Sprintf("the response from %s is longer than %d bytes", req.URL.Host, limit)}
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: raw, resp: resp}, nil
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
	return Call(ctx, client, Request{Method: method, URL: url, Header: headers, Body: body, Length: length, ContentType: contentType}, out)
}

// Call sends r and decodes a JSON response into out (if not nil). A
// non-2xx response becomes a classified *Error.
func Call(ctx context.Context, client *http.Client, r Request, out any) error {
	resp, err := Fetch(ctx, client, r)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			// The platform accepted the request; we just cannot read its
			// answer, so whether it took effect is unknown.
			return &Error{Kind: afterSending(resp.resp.Request), Code: "decode", Msg: "unreadable response", Err: err}
		}
	}
	return nil
}

// RequireID returns nil if id is set, and otherwise an Uncertain error: the
// platform accepted the post but did not say which one it is, so it may be
// public, and it is neither recorded as published without a reference nor
// retried blindly (ADR 0011).
func RequireID(id, what string) error {
	if id != "" {
		return nil
	}
	return &Error{Kind: Uncertain, Code: "no_id", Msg: what + " answered without an ID"}
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

// ServerError classifies a 5xx response. A read can be retried (Transient),
// but a write may have been carried out before the platform failed: X and
// Meta are known to save a post and answer 500. So a 5xx to any other
// method is Uncertain, and a non-idempotent platform is not retried
// (ADR 0011), unless the request was marked RetrySafe.
func ServerError(resp *http.Response, code, msg string) *Error {
	return &Error{Kind: afterSending(resp.Request), Code: code, Msg: msg, Status: resp.StatusCode}
}

// Classify turns an error response into a *Error, keeping its status and
// the start of its body for adapters that read a platform's own codes.
func Classify(resp *http.Response, body []byte) *Error {
	msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body))
	var e *Error
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e = &Error{Kind: RateLimited, Code: "rate_limited", Msg: msg, RetryAfter: RetryAfter(resp.Header, time.Now())}
	case resp.StatusCode == http.StatusUnauthorized:
		e = &Error{Kind: AuthRevoked, Code: "unauthorized", Msg: msg}
	case resp.StatusCode >= 500:
		e = ServerError(resp, "server_error", msg)
		e.RetryAfter = RetryAfter(resp.Header, time.Now())
	default:
		e = &Error{Kind: Rejected, Code: "rejected", Msg: msg}
	}
	e.Status = resp.StatusCode
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	e.Body = bytes.Clone(body)
	return e
}

// HTTPStatus is err's HTTP status, if it is a *Error from a response; 0
// otherwise.
func HTTPStatus(err error) int {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Status
	}
	return 0
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
