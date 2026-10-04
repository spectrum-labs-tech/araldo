// SPDX-License-Identifier: AGPL-3.0-or-later

package netguard

import (
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

	guarded := Client(false, 2*time.Second)
	resp, err := guarded.Get(srv.URL) //nolint:noctx // test
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("guarded client reached a loopback server")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("err = %v", err)
	}

	open := Client(true, 2*time.Second)
	resp, err = open.Get(srv.URL) //nolint:noctx // test
	if err != nil {
		t.Fatalf("allowPrivate client: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := Client(true, 2*time.Second).Get(srv.URL) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d: the redirect was followed", resp.StatusCode)
	}
}
