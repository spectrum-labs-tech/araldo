// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// TestListen streams events, prints them, forwards each signed to a local
// receiver, reconnects after the stream drops (resuming after the last
// event), and stops when the server refuses.
func TestListen(t *testing.T) {
	var mu sync.Mutex
	var connects []string // each connection's Last-Event-ID
	araldo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" || r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		connects = append(connects, r.Header.Get("Last-Event-ID"))
		n := len(connects)
		mu.Unlock()
		switch n {
		case 1: // two events, then the connection drops
			if r.URL.Query().Get("types") != "post.created,post.published" {
				t.Errorf("types = %q", r.URL.Query().Get("types"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "retry: 3000\n: connected\n\n"+
				"id: evt_1\nevent: post.created\ndata: {\"id\":\"evt_1\",\"type\":\"post.created\"}\n\n"+
				": ping\n\n"+
				"id: evt_2\nevent: post.published\ndata: {\"id\":\"evt_2\",\"type\":\"post.published\"}\n\n")
		default: // the credential was revoked meanwhile
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"api_key_invalid","detail":"The key was revoked.","status":401}`)
		}
	}))
	defer araldo.Close()

	var got []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Header.Get("Araldo-Signature")+"|"+r.Header.Get("Araldo-Event-Id")+"|"+string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	cliEnv(t, araldo, true)
	old := listenRetry
	listenRetry = 10 * time.Millisecond
	defer func() { listenRetry = old }()

	code, out, errOut := runCLI(t, "", "listen", "--forward-to", receiver.URL+"/hooks", "--events", "post.created, post.published")
	if code == ExitOK || !strings.Contains(errOut, "revoked") {
		t.Fatalf("listen: exit %d, want it to stop at the refusal\n%s", code, errOut)
	}
	secret := between(errOut, "Your webhook signing secret is ", " (")
	if !strings.HasPrefix(secret, "whsec_") || !strings.Contains(errOut, "Forwarding to "+receiver.URL+"/hooks") {
		t.Fatalf("the banner: %q", errOut)
	}
	for _, want := range []string{"--> post.created [evt_1]", "--> post.published [evt_2]", "<--  [204] POST " + receiver.URL + "/hooks [evt_2]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "reconnecting") {
		t.Errorf("no word of the reconnect: %q", errOut)
	}
	mu.Lock()
	resumed, received := append([]string(nil), connects...), append([]string(nil), got...)
	mu.Unlock()
	if len(resumed) != 2 || resumed[0] != "" || resumed[1] != "evt_2" {
		t.Fatalf("connections resumed after %q", resumed)
	}
	if len(received) != 2 {
		t.Fatalf("the receiver got %d events", len(received))
	}
	for _, g := range received {
		parts := strings.SplitN(g, "|", 3)
		if err := core.VerifySignature(secret, parts[0], []byte(parts[2]), time.Now(), time.Minute); err != nil {
			t.Errorf("event %s: the signature does not verify with the printed secret: %v", parts[1], err)
		}
	}

	// The secret is this computer's, kept: a receiver is set up once.
	_, _, errOut = runCLI(t, "", "listen")
	if again := between(errOut, "Your webhook signing secret is ", " ("); again != secret {
		t.Fatalf("a second listen used %q, not %q", again, secret)
	}
	if code, _, errOut = runCLI(t, "", "listen", "--forward-to", "localhost:3000"); code != ExitUsage || !strings.Contains(errOut, "not an http") {
		t.Fatalf("a URL without a scheme: exit %d %q", code, errOut)
	}
}

// between is the text of s between two markers.
func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}
