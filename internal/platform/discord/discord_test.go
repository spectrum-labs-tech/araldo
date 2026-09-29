// SPDX-License-Identifier: AGPL-3.0-or-later

package discord

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestPublish(t *testing.T) {
	t.Parallel()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/webhooks/1/tok":
			_, _ = w.Write([]byte(`{"id":"1","name":"Launches","channel_id":"c","guild_id":"g"}`))
		case r.Method == http.MethodPost && r.URL.Query().Get("wait") == "true":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"id":"m1","channel_id":"c"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AllowAnyHost = true
	creds := platform.Credentials{"webhook_url": srv.URL + "/api/webhooks/1/tok"}
	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.Handle != "Launches" || acct.URL != "https://discord.com/channels/g/c" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"@everyone shipped"}}, nil)
	if err != nil || len(res.Parts) != 1 || res.Parts[0].ID != "m1" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	mentions, _ := got["allowed_mentions"].(map[string]any)
	if parse, _ := mentions["parse"].([]any); parse == nil || len(parse) != 0 {
		t.Fatalf("allowed_mentions = %v, want mentions disabled", got["allowed_mentions"])
	}
}

func TestWebhookURLMustBeDiscord(t *testing.T) {
	t.Parallel()
	a := New(http.DefaultClient)
	for _, u := range []string{"", "http://discord.com/api/webhooks/1/x", "https://evil.test/api/webhooks/1/x", "https://discord.com/other"} {
		if _, err := a.Verify(t.Context(), platform.Credentials{"webhook_url": u}); platform.KindOf(err) != platform.AuthRevoked {
			t.Errorf("Verify(%q) = %v, want auth_revoked", u, err)
		}
	}
}

func TestDeletedWebhookNeedsReauth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown Webhook","code":10015}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AllowAnyHost = true
	_, err := a.Publish(t.Context(), platform.Credentials{"webhook_url": srv.URL + "/api/webhooks/1/x"}, platform.Payload{Parts: []string{"x"}}, nil)
	if platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Publish = %v, want auth_revoked", err)
	}
}
