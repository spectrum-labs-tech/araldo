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
		// Ranges net.IP has no method for.
		"192.0.0.8":       false, // IETF protocol assignments
		"192.0.2.1":       false, // documentation
		"198.18.0.1":      false, // benchmarking
		"198.19.255.255":  false,
		"198.20.0.1":      true,
		"240.0.0.1":       false, // reserved
		"255.255.255.255": false,
		"2001:db8::1":     false,
		"2001::1":         false, // Teredo
		"::ffff:10.0.0.1": false, // IPv4-mapped
		"::ffff:8.8.8.8":  true,
		"::10.0.0.1":      false, // IPv4-compatible
		// NAT64 and 6to4 reach the IPv4 address they carry.
		"64:ff9b::808:808":   true,  // 8.8.8.8
		"64:ff9b::7f00:1":    false, // 127.0.0.1
		"64:ff9b::a9fe:a9fe": false, // 169.254.169.254
		"64:ff9b::a00:1":     false, // 10.0.0.1
		"64:ff9b:1::a00:1":   false, // local-use NAT64
		"2002:808:808::1":    true,  // 8.8.8.8
		"2002:7f00:1::1":     false, // 127.0.0.1
		"2002:a9fe:a9fe::1":  false, // 169.254.169.254
		"2002:c0a8:101::1":   false, // 192.168.1.1
		// Cloud metadata, wherever it answers.
		"fd00:ec2::254":   false,
		"100.100.100.200": false,
		"168.63.129.16":   false,
		"168.63.129.17":   true,
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
		// Metadata services stay out of reach under All, and under a
		// network that merely contains them: only their own address names
		// them.
		{"true", map[string]bool{"fd00:ec2::254": false, "100.100.100.200": false, "168.63.129.16": false, "169.254.170.2": false,
			"100.100.100.100": true, "fd00:ec2::253": true}},
		{"fd00::/8, 100.64.0.0/10", map[string]bool{"fd00:ec2::254": false, "100.100.100.200": false, "fd00::1": true, "100.100.100.100": true}},
		{"fd00:ec2::254, 100.100.100.200", map[string]bool{"fd00:ec2::254": true, "100.100.100.200": true, "fd00::1": false}},
		{"false", map[string]bool{"168.63.129.16": false, "64:ff9b::a9fe:a9fe": false}},
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
			time.Sleep(3 * time.Second)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	// Margins wide enough for a loaded machine: an answer within a second
	// of the body, an upload taking two, a server silent for three.
	c := UploadClient(Policy{All: true}, time.Second)

	// About 2s to send, twice the wait: fine, the wait starts once sent.
	resp, err := c.Post(srv.URL+"/upload", "video/mp4", &slowReader{left: 40, pause: 200 * time.Millisecond})
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
