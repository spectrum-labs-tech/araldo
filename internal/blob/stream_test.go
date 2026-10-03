// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeMultipart is an S3 bucket that takes multipart uploads and ranges.
type fakeMultipart struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	aborted int
	// failPart fails the upload of this part number.
	failPart int
}

func (f *fakeMultipart) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := fmt.Sprintf("up%d", len(f.uploads)+1)
		f.uploads[id] = map[int][]byte{}
		_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == http.MethodPut && q.Has("partNumber"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		if n == f.failPart {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.uploads[q.Get("uploadId")][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"etag%d"`, n))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		var done struct {
			Parts []completePart `xml:"Part"`
		}
		_ = xml.Unmarshal(body, &done)
		var all []byte
		for i, p := range done.Parts {
			if p.PartNumber != i+1 || p.ETag != fmt.Sprintf(`"etag%d"`, i+1) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			all = append(all, f.uploads[q.Get("uploadId")][p.PartNumber]...)
		}
		f.objects[r.URL.Path] = all
		_, _ = w.Write([]byte("<CompleteMultipartUploadResult/>"))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		f.aborted++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objects[r.URL.Path] = body
	case r.Method == http.MethodGet:
		obj, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var from, to int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to); err == nil {
			if from >= len(obj) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(obj[from:min(to+1, len(obj))])
			return
		}
		_, _ = w.Write(obj)
	}
}

func multipartBucket(t *testing.T) (*fakeMultipart, *S3) {
	t.Helper()
	f := &fakeMultipart{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s, err := NewS3(srv.URL, "araldo", "", "ak", "sk", "")
	if err != nil {
		t.Fatal(err)
	}
	s.PartSize = 10
	return f, s
}

func TestPutStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
	}{
		{"one request", 7},
		{"exactly one part", 10},
		{"parts and a short last one", 25},
		{"whole parts", 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, s := multipartBucket(t)
			data := bytes.Repeat([]byte("v"), tt.size)
			n, sum, err := s.PutStream(t.Context(), "v.mp4", bytes.NewReader(data), "video/mp4", 100)
			want := sha256.Sum256(data)
			if err != nil || n != int64(tt.size) || !bytes.Equal(sum, want[:]) || !bytes.Equal(f.objects["/araldo/v.mp4"], data) {
				t.Fatalf("stored %d bytes (%v): %q", n, err, f.objects["/araldo/v.mp4"])
			}
		})
	}
}

func TestPutStreamAbortsOnFailure(t *testing.T) {
	t.Parallel()
	f, s := multipartBucket(t)
	if _, _, err := s.PutStream(t.Context(), "big.mp4", bytes.NewReader(make([]byte, 35)), "video/mp4", 30); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	f.failPart = 2
	if _, _, err := s.PutStream(t.Context(), "fail.mp4", bytes.NewReader(make([]byte, 25)), "video/mp4", 100); err == nil {
		t.Fatal("a failed part should fail the upload")
	}
	if _, _, err := s.PutStream(t.Context(), "tiny.mp4", strings.NewReader(strings.Repeat("x", 20)), "video/mp4", 15); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit at the second part: %v", err)
	}
	if f.aborted != 3 || len(f.uploads) != 0 || len(f.objects) != 0 {
		t.Fatalf("aborted %d, open uploads %d, objects %d", f.aborted, len(f.uploads), len(f.objects))
	}
}

func TestGetRange(t *testing.T) {
	t.Parallel()
	f, s := multipartBucket(t)
	f.objects["/araldo/k"] = []byte("0123456789")
	tests := []struct {
		off, n int64
		want   string
	}{
		{0, 4, "0123"},
		{8, 5, "89"},
	}
	for _, tt := range tests {
		if got, err := s.GetRange(t.Context(), "k", tt.off, tt.n); err != nil || string(got) != tt.want {
			t.Errorf("GetRange(%d, %d) = %q, %v", tt.off, tt.n, got, err)
		}
	}
	if _, err := s.GetRange(t.Context(), "k", 20, 4); !errors.Is(err, io.EOF) {
		t.Errorf("past the end: %v", err)
	}
	if _, err := s.GetRange(t.Context(), "missing", 0, 4); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing object: %v", err)
	}
}
