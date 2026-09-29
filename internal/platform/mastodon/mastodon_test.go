// SPDX-License-Identifier: AGPL-3.0-or-later

package mastodon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestPublishThreadWithIdempotencyKeys(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var keys, replies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/accounts/verify_credentials":
			_, _ = w.Write([]byte(`{"id":"1","acct":"araldo","display_name":"Araldo","url":"https://m.test/@araldo"}`))
		case "/api/v1/statuses":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			reply, _ := in["in_reply_to_id"].(string)
			replies = append(replies, reply)
			n := len(keys)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"id": string(rune('0' + n)), "url": "https://m.test/@araldo/" + string(rune('0'+n))})
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	creds := platform.Credentials{"instance": srv.URL, "access_token": "tok"}

	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.DisplayName != "Araldo" || acct.ExternalID != "1" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Key: "ptgt_9", Parts: []string{"a", "b"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://m.test/@araldo/1" {
		t.Fatalf("permalink %q", res.Permalink)
	}
	if keys[0] != "ptgt_9-0" || keys[1] != "ptgt_9-1" {
		t.Fatalf("idempotency keys %q", keys)
	}
	if replies[0] != "" || replies[1] != "1" {
		t.Fatalf("in_reply_to %q, want the second part to reply to the first", replies)
	}
}

func TestBadTokenNeedsReauth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	a := New(srv.Client())
	if _, err := a.Verify(t.Context(), platform.Credentials{"instance": srv.URL, "access_token": "bad"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Verify = %v, want auth_revoked", err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{"instance": "::::", "access_token": "x"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Verify(bad URL) = %v", err)
	}
}
