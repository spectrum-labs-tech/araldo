// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Operations ADR 0019 added to the API: channel recovery, slots, attempt
// history, strict query parameters and idempotency after errors.

func TestChannelRecovery(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, ch := c.json(http.MethodPost, "/v1/channels/"+c.channel, map[string]any{"status": "disabled"})
	if status != http.StatusOK || ch["status"] != "disabled" {
		t.Fatalf("disable: %d %v", status, ch)
	}
	if settings, _ := ch["settings"].(map[string]any); settings["emulates"] != "bluesky" {
		t.Fatalf("settings %v, want the non-secret connect fields", ch["settings"])
	}
	status, ch = c.json(http.MethodPost, "/v1/channels/"+c.channel, map[string]any{"status": "active"})
	if status != http.StatusOK || ch["status"] != "active" {
		t.Fatalf("enable: %d %v", status, ch)
	}
	status, got := c.json(http.MethodPost, "/v1/channels/"+c.channel, map[string]any{"status": "needs_reauth"})
	if status != http.StatusBadRequest || got["code"] != "parameter_invalid" {
		t.Fatalf("setting needs_reauth: %d %v", status, got)
	}
	// Reconnecting with no fields keeps the settings.
	status, ch = c.json(http.MethodPost, "/v1/channels/"+c.channel+"/reconnect", map[string]any{"fields": map[string]string{}})
	if status != http.StatusOK || ch["status"] != "active" || ch["emulates"] != "bluesky" {
		t.Fatalf("reconnect: %d %v", status, ch)
	}
	status, ch = c.json(http.MethodPost, "/v1/channels/"+c.channel+"/reconnect", map[string]any{"fields": map[string]string{"emulates": "mastodon"}})
	if settings, _ := ch["settings"].(map[string]any); status != http.StatusOK || settings["emulates"] != "mastodon" {
		t.Fatalf("reconnect with a new setting: %d %v", status, ch)
	}
}

func TestBrandSlots(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, b := c.json(http.MethodGet, "/v1/brands/"+c.brand, nil)
	if slots, _ := b["slots"].([]any); status != http.StatusOK || len(slots) != 10 {
		t.Fatalf("a new brand's default slots: %d %v", status, b["slots"])
	}
	status, b = c.json(http.MethodPost, "/v1/brands/"+c.brand, map[string]any{"slots": []map[string]string{{"weekday": "saturday", "time": "10:30"}}})
	slots, _ := b["slots"].([]any)
	if status != http.StatusOK || len(slots) != 1 || fmt.Sprint(slots[0]) != "map[time:10:30 weekday:saturday]" {
		t.Fatalf("set slots: %d %v", status, b["slots"])
	}
	// Leaving slots out keeps them.
	status, b = c.json(http.MethodPost, "/v1/brands/"+c.brand, map[string]any{"name": "Renamed"})
	if slots, _ := b["slots"].([]any); status != http.StatusOK || len(slots) != 1 {
		t.Fatalf("update without slots: %d %v", status, b["slots"])
	}
	status, got := c.json(http.MethodPost, "/v1/brands/"+c.brand, map[string]any{"slots": []map[string]string{{"weekday": "someday", "time": "25:00"}}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(got["errors"]), "slot_invalid") {
		t.Fatalf("bad slot: %d %v", status, got)
	}
	// next_slot uses them: the next Saturday at 10:30 in the brand's zone (UTC).
	status, post := c.json(http.MethodPost, "/v1/posts", map[string]any{"brand": c.brand, "content": map[string]any{"body": "weekend"}, "publish_at": "next_slot"})
	at, _ := time.Parse(time.RFC3339, fmt.Sprint(post["publish_at"]))
	if status != http.StatusCreated || at.Weekday() != time.Saturday || at.Hour() != 10 || at.Minute() != 30 {
		t.Fatalf("next_slot: %d %v", status, post["publish_at"])
	}
	// A new brand can be created with its slots.
	status, nb := c.json(http.MethodPost, "/v1/brands", map[string]any{"name": "Otium " + uuid.NewString()[:6], "slots": []map[string]string{}})
	if slots, ok := nb["slots"].([]any); status != http.StatusCreated || !ok || len(slots) != 0 {
		t.Fatalf("create with no slots: %d %v", status, nb)
	}
}

func TestAttemptHistory(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, post := c.json(http.MethodPost, "/v1/posts", map[string]any{"brand": c.brand, "content": map[string]any{"body": "attempts"}})
	if status != http.StatusCreated {
		t.Fatalf("post: %d %v", status, post)
	}
	target := post["targets"].([]any)[0].(map[string]any)["id"].(string)
	pid, _ := id.Parse(id.Post, post["id"].(string))
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := c.s.PublishDue(t.Context(), "api-test"); err != nil {
			t.Fatal(err)
		}
		p, err := c.s.Post(t.Context(), c.owner, pid)
		if err != nil {
			t.Fatal(err)
		}
		if p.Targets[0].Status == "published" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not published: %s", p.Targets[0].Status)
		}
	}
	status, got := c.do(http.MethodGet, "/v1/post_targets/"+target+"/attempts", "", nil, nil)
	data, _ := got["data"].([]any)
	if status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["outcome"] != "published" {
		t.Fatalf("attempts: %d %v", status, got)
	}
}

func TestStrictQueryAndIdempotencyAfterErrors(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, got := c.do(http.MethodGet, "/v1/webhook_endpoints?brand="+c.brand, "", nil, nil)
	if status != http.StatusBadRequest || got["code"] != "parameter_unknown" || got["param"] != "brand" {
		t.Fatalf("a filter the route does not have: %d %v", status, got)
	}
	if status, _ := c.do(http.MethodGet, "/v1/posts?metadata[build_id]=1&limit=5", "", nil, nil); status != http.StatusOK {
		t.Fatalf("metadata filter: %d", status)
	}

	// A failed request releases its key, so the corrected one can use it.
	key := map[string]string{"Idempotency-Key": uuid.NewString()}
	bad := `{"brand":"` + c.brand + `","content":{"body":""}}`
	good := `{"brand":"` + c.brand + `","content":{"body":"fixed"}}`
	if status, _ := c.do(http.MethodPost, "/v1/posts", "application/json", []byte(bad), key); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad post: %d", status)
	}
	status, first := c.do(http.MethodPost, "/v1/posts", "application/json", []byte(good), key)
	if status != http.StatusCreated {
		t.Fatalf("corrected post with the same key: %d %v", status, first)
	}
	status, again := c.do(http.MethodPost, "/v1/posts", "application/json", []byte(good), key)
	if status != http.StatusCreated || again["id"] != first["id"] {
		t.Fatalf("replay: %d %v, want the first response", status, again["id"])
	}
}

func TestTemplatesByKey(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, tmpl := c.json(http.MethodPost, "/v1/templates", map[string]any{"brand": c.brand, "key": "release", "body": "Shipped {{.version}}",
		"variables": map[string]any{"type": "object", "properties": map[string]any{"version": map[string]any{"type": "string"}}}})
	if status != http.StatusCreated {
		t.Fatalf("template: %d %v", status, tmpl)
	}
	status, got := c.do(http.MethodGet, "/v1/templates?brand="+c.brand+"&key=release", "", nil, nil)
	if data, _ := got["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != tmpl["id"] {
		t.Fatalf("by key: %d %v", status, got)
	}
	status, got = c.json(http.MethodPost, "/v1/templates/release/versions", map[string]any{"body": "x"})
	if status != http.StatusNotFound || !strings.Contains(fmt.Sprint(got["detail"]), "GET /v1/templates?brand=") {
		t.Fatalf("a key in the path: %d %v", status, got)
	}
}
