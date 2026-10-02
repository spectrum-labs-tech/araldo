// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blob stores media files in S3-compatible object storage (ADR
// 0017): AWS S3, Cloudflare R2, MinIO and the like. It signs requests
// itself (Signature Version 4) instead of depending on an SDK, and uses
// path-style URLs ({endpoint}/{bucket}/{key}), which they all accept.
package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned by Get for a missing object.
var ErrNotFound = errors.New("blob: object not found")

// S3 is a bucket in S3-compatible storage.
type S3 struct {
	// Endpoint is the service URL, e.g. https://<account>.r2.cloudflarestorage.com.
	Endpoint  *url.URL
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	// Prefix goes before every key (for example "media/").
	Prefix string
	Client *http.Client
	// Now is the clock for signatures; tests replace it.
	Now func() time.Time
}

// NewS3 checks the settings and returns a bucket. region may be empty
// ("auto", which R2 expects; AWS needs the bucket's region).
func NewS3(endpoint, bucket, region, accessKey, secretKey, prefix string) (*S3, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("blob: endpoint %q is not an http(s) URL", endpoint)
	}
	if bucket == "" || accessKey == "" || secretKey == "" {
		return nil, errors.New("blob: bucket, access key and secret key are required")
	}
	if region == "" {
		region = "auto"
	}
	return &S3{Endpoint: u, Bucket: bucket, Region: region, AccessKey: accessKey, SecretKey: secretKey, Prefix: prefix,
		Client: &http.Client{Timeout: 60 * time.Second}, Now: time.Now}, nil
}

// Put stores data at key.
func (s *S3) Put(ctx context.Context, key string, data []byte, contentType string) error {
	resp, err := s.do(ctx, http.MethodPut, key, data, contentType)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return s.failure(resp, "put", key)
	}
	return nil
}

// Get returns the object at key; the caller closes it.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, key, nil, "")
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	case resp.StatusCode/100 != 2:
		defer drain(resp)
		return nil, s.failure(resp, "get", key)
	}
	return resp.Body, nil
}

// Delete removes the object at key; a missing object is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil, "")
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return s.failure(resp, "delete", key)
	}
	return nil
}

func (s *S3) do(ctx context.Context, method, key string, body []byte, contentType string) (*http.Response, error) {
	u := *s.Endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/" + s.Bucket + "/" + s.Prefix + key
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body == nil {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	sum := sha256.Sum256(body)
	Sign(req, s.AccessKey, s.SecretKey, s.Region, hex.EncodeToString(sum[:]), s.Now())
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: %s %s: %w", method, key, err)
	}
	return resp, nil
}

func (s *S3) failure(resp *http.Response, op, key string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("blob: %s %s: HTTP %d: %s", op, key, resp.StatusCode, strings.TrimSpace(string(b)))
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// Sign adds AWS Signature Version 4 headers to req for the S3 service:
// x-amz-date, x-amz-content-sha256 and Authorization. It signs the host,
// Content-Type, Range and every x-amz-* header
// (https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html).
func Sign(req *http.Request, accessKey, secretKey, region, payloadHash string, now time.Time) {
	stamp := now.UTC().Format("20060102T150405Z")
	date := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	headers := map[string]string{"host": req.URL.Host}
	for name, vals := range req.Header {
		lower := strings.ToLower(name)
		if lower == "content-type" || lower == "range" || strings.HasPrefix(lower, "x-amz-") {
			headers[lower] = strings.TrimSpace(strings.Join(vals, ","))
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + headers[n] + "\n")
	}
	signed := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		encodePath(req.URL.EscapedPath()),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := date + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	key := hmacSHA256([]byte("AWS4"+secretKey), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// encodePath re-encodes an escaped path the way S3 signs it: each segment
// URI-encoded once, with only unreserved characters left bare.
func encodePath(escaped string) string {
	if escaped == "" {
		return "/"
	}
	segs := strings.Split(escaped, "/")
	for i, seg := range segs {
		raw, err := url.PathUnescape(seg)
		if err != nil {
			raw = seg
		}
		segs[i] = uriEncode(raw)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
