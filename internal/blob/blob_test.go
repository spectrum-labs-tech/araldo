// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	exampleAccess = "AKIAIOSFODNN7EXAMPLE"
	exampleSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	emptyHash     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// TestSignMatchesAWS checks the signer against the worked example in the
// S3 documentation ("Example: GET Object",
// https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html).
func TestSignMatchesAWS(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	Sign(req, exampleAccess, exampleSecret, "us-east-1", emptyHash, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestEncoding(t *testing.T) {
	t.Parallel()
	if got := encodePath("/bucket/a%20b/c~d"); got != "/bucket/a%20b/c~d" {
		t.Errorf("encodePath = %q", got)
	}
	if got := encodePath(""); got != "/" {
		t.Errorf("encodePath(empty) = %q", got)
	}
	if got := canonicalQuery(map[string][]string{"b": {"2"}, "a": {"x y", "1"}}); got != "a=1&a=x%20y&b=2" {
		t.Errorf("canonicalQuery = %q", got)
	}
}

// fakeS3 keeps objects in memory and checks each request is signed with
// the payload it carries.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string
	types   map[string]string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ak/") ||
		r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error><Code>SignatureDoesNotMatch</Code></Error>"))
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.objects[r.URL.Path], f.types[r.URL.Path] = string(body), r.Header.Get("Content-Type")
	case http.MethodGet:
		obj, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(obj))
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestS3RoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeS3{objects: map[string]string{}, types: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s, err := NewS3(srv.URL+"/", "araldo", "", "ak", "sk", "media/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := s.Put(ctx, "org/one.png", []byte("png bytes"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if f.objects["/araldo/media/org/one.png"] != "png bytes" || f.types["/araldo/media/org/one.png"] != "image/png" {
		t.Fatalf("stored %v %v", f.objects, f.types)
	}
	rc, err := s.Get(ctx, "org/one.png")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "png bytes" {
		t.Fatalf("Get = %q", got)
	}
	if err := s.Delete(ctx, "org/one.png"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "org/one.png"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	if _, err := s.Get(ctx, "org/one.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestS3ReportsFailures(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(&fakeS3{objects: map[string]string{}, types: map[string]string{}})
	defer srv.Close()
	s, _ := NewS3(srv.URL, "araldo", "auto", "wrong", "sk", "")
	err := s.Put(t.Context(), "k", []byte("x"), "image/png")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("Put with a bad key = %v", err)
	}
}

func TestNewS3Validates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ endpoint, bucket, access, secret string }{
		{"", "b", "a", "s"},
		{"ftp://x", "b", "a", "s"},
		{"https://x", "", "a", "s"},
		{"https://x", "b", "", "s"},
		{"https://x", "b", "a", ""},
	} {
		if _, err := NewS3(tt.endpoint, tt.bucket, "", tt.access, tt.secret, ""); err == nil {
			t.Errorf("NewS3(%+v) accepted", tt)
		}
	}
}
