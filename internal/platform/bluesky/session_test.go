// SPDX-License-Identifier: AGPL-3.0-or-later

package bluesky

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeSessions is a PDS that issues expiring sessions and counts sign-ins
// and renewals.
type fakeSessions struct {
	mu            sync.Mutex
	now           time.Time
	n             int
	valid         map[string]bool // access tokens the server accepts
	refreshTok    string          // the refresh token it accepts
	logins        int
	refreshes     int
	refuseRefresh bool
	records       map[string]bool
}

func jwtExpiring(exp time.Time, n int) string {
	claims, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "n": n})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func (f *fakeSessions) issue(w http.ResponseWriter) {
	f.n++
	access := jwtExpiring(f.now.Add(2*time.Hour), f.n)
	f.valid[access] = true
	f.refreshTok = fmt.Sprintf("refresh-%d", f.n)
	_ = json.NewEncoder(w).Encode(map[string]string{"did": "did:plc:abc", "handle": "araldo.test", "accessJwt": access, "refreshJwt": f.refreshTok})
}

func (f *fakeSessions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	expired := func() {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"ExpiredToken","message":"Token has expired"}`))
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch r.URL.Path {
	case "/xrpc/com.atproto.server.createSession":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["password"] != "app-pass" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"AuthenticationRequired"}`))
			return
		}
		f.logins++
		f.issue(w)
	case "/xrpc/com.atproto.server.refreshSession":
		if f.refuseRefresh || tok != f.refreshTok {
			expired()
			return
		}
		f.refreshes++
		f.issue(w)
	case "/xrpc/com.atproto.server.getSession":
		if !f.valid[tok] {
			expired()
			return
		}
		_, _ = w.Write([]byte(`{"did":"did:plc:abc","handle":"araldo.test"}`))
	case "/xrpc/com.atproto.repo.getRecord":
		if !f.valid[tok] {
			expired()
			return
		}
		if !f.records[r.URL.Query().Get("rkey")] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"RecordNotFound"}`))
			return
		}
		_, _ = w.Write([]byte(`{"uri":"at://did:plc:abc/app.bsky.feed.post/x","cid":"c"}`))
	case "/xrpc/com.atproto.repo.createRecord":
		if !f.valid[tok] {
			expired()
			return
		}
		var in struct {
			Rkey string `json:"rkey"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.records[in.Rkey] = true
		_, _ = w.Write([]byte(`{"uri":"at://did:plc:abc/app.bsky.feed.post/x","cid":"c"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSessions) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// revoke makes the server refuse every access token it has issued.
func (f *fakeSessions) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = map[string]bool{}
}

func (f *fakeSessions) counts() (logins, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins, f.refreshes
}

func sessionSetup(t *testing.T) (*fakeSessions, *Adapter, platform.Credentials) {
	t.Helper()
	f := &fakeSessions{now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), valid: map[string]bool{}, records: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.Now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}
	return f, a, platform.Credentials{"identifier": "araldo.test", "app_password": "app-pass", "service": srv.URL}
}

func payload(n int) platform.Payload {
	return platform.Payload{Key: fmt.Sprintf("ptgt_%d", n), KeyTime: time.Unix(1_700_000_000, 0), Parts: []string{"hi"}}
}

func TestSessionIsReused(t *testing.T) {
	t.Parallel()
	f, a, creds := sessionSetup(t)
	for i := range 3 {
		if _, err := a.Publish(t.Context(), creds, payload(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Verify(t.Context(), creds); err != nil {
		t.Fatal(err)
	}
	if logins, refreshes := f.counts(); logins != 1 || refreshes != 0 {
		t.Fatalf("%d sign-ins and %d renewals, want 1 and 0", logins, refreshes)
	}
}

func TestSessionRenewal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		refuseRefresh bool
		logins        int
		refreshes     int
	}{
		{"renewed with the refresh token", false, 1, 1},
		{"signed in again when renewal is refused", true, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, a, creds := sessionSetup(t)
			if _, err := a.Publish(t.Context(), creds, payload(1), nil); err != nil {
				t.Fatal(err)
			}
			f.refuseRefresh = tt.refuseRefresh
			f.advance(2*time.Hour - time.Minute) // within renewAhead of expiry
			if _, err := a.Publish(t.Context(), creds, payload(2), nil); err != nil {
				t.Fatal(err)
			}
			if logins, refreshes := f.counts(); logins != tt.logins || refreshes != tt.refreshes {
				t.Fatalf("%d sign-ins and %d renewals, want %d and %d", logins, refreshes, tt.logins, tt.refreshes)
			}
		})
	}
}

func TestStaleSession(t *testing.T) {
	t.Parallel()
	f, a, creds := sessionSetup(t)
	if _, err := a.Publish(t.Context(), creds, payload(1), nil); err != nil {
		t.Fatal(err)
	}
	f.revoke()
	// The cached token is refused: try again, never "reconnect the channel".
	if _, err := a.Publish(t.Context(), creds, payload(2), nil); platform.KindOf(err) != platform.Transient {
		t.Fatalf("a stale cached session: %v, want transient", err)
	}
	if _, err := a.Publish(t.Context(), creds, payload(2), nil); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	f.revoke()
	if _, err := a.Verify(t.Context(), creds); err != nil {
		t.Fatalf("Verify with a stale cached session: %v", err)
	}
	if logins, _ := f.counts(); logins != 3 {
		t.Fatalf("%d sign-ins, want 3", logins)
	}
	// New credentials never use the old session.
	bad := platform.Credentials{"identifier": creds["identifier"], "app_password": "changed", "service": creds["service"]}
	if _, err := a.Verify(t.Context(), bad); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a changed password: %v", err)
	}
}

func TestSessionCacheIsBounded(t *testing.T) {
	t.Parallel()
	var c sessionCache
	for i := range maxSessions + 10 {
		c.entry(keyOf(platform.Credentials{"identifier": fmt.Sprint(i), "app_password": "p"}))
	}
	if n := c.len(); n != maxSessions {
		t.Fatalf("%d sessions, want %d", n, maxSessions)
	}
}

func TestExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		jwt  string
		want time.Time
	}{
		{jwtExpiring(now.Add(2*time.Hour), 1), now.Add(2 * time.Hour)},
		{"not-a-jwt", now.Add(unknownLife)},
		{"a.!!!.c", now.Add(unknownLife)},
		{"a." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".c", now.Add(unknownLife)},
	}
	for _, tt := range tests {
		if got := expiry(tt.jwt, now); !got.Equal(tt.want) {
			t.Errorf("expiry(%q) = %s, want %s", tt.jwt, got, tt.want)
		}
	}
}
