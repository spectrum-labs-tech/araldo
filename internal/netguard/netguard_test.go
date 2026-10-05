// SPDX-License-Identifier: AGPL-3.0-or-later

package netguard

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"8.8.8.8":         true,
		"2606:4700::1111": true,
		"127.0.0.1":       false,
		"10.1.2.3":        false,
		"192.168.0.10":    false,
		"172.16.0.1":      false,
		"169.254.169.254": false, // cloud metadata
		"100.64.1.2":      false, // tailnet (CGNAT)
		"0.0.0.0":         false,
		"::1":             false,
		"fd00::1":         false,
		"fe80::1":         false,
	}
	for addr, want := range tests {
		if got := Public(net.ParseIP(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
	if Public(nil) {
		t.Error("Public(nil) = true")
	}
}

func TestClientRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()

	guarded := Client(Policy{}, 2*time.Second)
	resp, err := guarded.Get(srv.URL) //nolint:noctx // test
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("guarded client reached a loopback server")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("err = %v", err)
	}

	open := Client(Policy{All: true}, 2*time.Second)
	resp, err = open.Get(srv.URL) //nolint:noctx // test
	if err != nil {
		t.Fatalf("allowPrivate client: %v", err)
	}
	_ = resp.Body.Close()
}

func TestPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		setting string
		allowed map[string]bool
	}{
		{"", map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "192.168.1.20": false}},
		{"false", map[string]bool{"8.8.8.8": true, "10.0.0.1": false}},
		{"true", map[string]bool{
			"8.8.8.8": true, "127.0.0.1": true, "192.168.1.20": true, "fd00::1": true,
			"169.254.169.254": false, // cloud metadata stays out of reach
			"fe80::1":         false,
		}},
		{"192.168.1.20, 10.8.0.0/16", map[string]bool{
			"8.8.8.8": true, "192.168.1.20": true, "10.8.3.4": true,
			"192.168.1.21": false, "10.9.0.1": false, "127.0.0.1": false, "169.254.169.254": false,
		}},
		{"169.254.169.254", map[string]bool{"169.254.169.254": true, "169.254.169.253": false}},
		{"fd00::/8", map[string]bool{"fd00::1": true, "::1": false}},
	}
	for _, tt := range tests {
		p, err := ParsePolicy(tt.setting)
		if err != nil {
			t.Fatalf("ParsePolicy(%q): %v", tt.setting, err)
		}
		for addr, want := range tt.allowed {
			if got := p.Allows(net.ParseIP(addr)); got != want {
				t.Errorf("%q allows %s = %t, want %t", tt.setting, addr, got, want)
			}
		}
	}
	for _, bad := range []string{"yes please", "10.0.0.0/33", "192.168.1.20,nope"} {
		if _, err := ParsePolicy(bad); err == nil {
			t.Errorf("ParsePolicy(%q) accepted it", bad)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := Client(Policy{All: true}, 2*time.Second).Get(srv.URL) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d: the redirect was followed", resp.StatusCode)
	}
}

// slowReader yields its bytes a few at a time, sleeping between reads.
type slowReader struct {
	left  int
	pause time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	n := min(len(p), r.left, 4)
	r.left -= n
	return n, nil
}

// TestUploadClient checks an upload may take longer than the wait for an
// answer, and a server that never answers is still given up on.
func TestUploadClient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/hang" {
			time.Sleep(time.Second)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := UploadClient(Policy{All: true}, 200*time.Millisecond)

	// About 400ms to send, twice the wait: fine, the wait starts once sent.
	resp, err := c.Post(srv.URL+"/upload", "video/mp4", &slowReader{left: 40, pause: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("a slow upload: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp, err = c.Post(srv.URL+"/hang", "video/mp4", strings.NewReader("x"))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a server that never answered was waited on")
	}
}
