// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestIdempotencyForUserTokens checks a person's CLI token dedupes POSTs
// with an Idempotency-Key, as an API key does.
func TestIdempotencyForUserTokens(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	h := tokenFor(t, c)
	h["Idempotency-Key"] = uuid.NewString()
	body := []byte(`{"brand":"` + c.brand + `","content":{"body":"once"},"publish_at":"next_slot"}`)
	status, first := c.do(http.MethodPost, "/v1/posts", "application/json", body, h)
	if status != http.StatusCreated {
		t.Fatalf("first: %d %v", status, first)
	}
	status, again := c.do(http.MethodPost, "/v1/posts", "application/json", body, h)
	if status != http.StatusCreated || again["id"] != first["id"] {
		t.Fatalf("retry: %d %v, want the first post %v", status, again["id"], first["id"])
	}
}

// TestSecretsAreNotReplayed checks a response holding a secret is not
// stored for replays: a retry with its key is told so, and the secret
// appears only in the first response.
func TestSecretsAreNotReplayed(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	keys := c
	h := map[string]string{"Idempotency-Key": uuid.NewString()}
	body := []byte(`{"url":"https://receiver.example/hooks"}`)
	status, first := keys.do(http.MethodPost, "/v1/webhook_endpoints", "application/json", body, h)
	secret, _ := first["secret"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("first: %d %v", status, first)
	}
	status, again := keys.do(http.MethodPost, "/v1/webhook_endpoints", "application/json", body, maps.Clone(h))
	if status != http.StatusConflict || again["code"] != "idempotency_response_withheld" || strings.Contains(strings.Join(mapStrings(again), " "), secret) {
		t.Fatalf("retry: %d %v, want idempotency_response_withheld without the secret", status, again)
	}
	var stored bool
	if err := shared.Pool().QueryRow(t.Context(), `SELECT response_body IS NOT NULL FROM idempotency_keys WHERE key = $1`,
		h["Idempotency-Key"]).Scan(&stored); err != nil || stored {
		t.Fatalf("the response body was stored (%v)", err)
	}
}

func mapStrings(m map[string]any) []string {
	var out []string
	for _, v := range m {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
