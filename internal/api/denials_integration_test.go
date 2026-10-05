// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// TestDenialsAreAudited checks a key refused for a missing scope is in the
// audit log, by route and code, once however often it retries.
func TestDenialsAreAudited(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	reader := c.keyWith("posts:read")
	for range 3 {
		if status, got := reader.do(http.MethodGet, "/v1/api_keys", "", nil, nil); status != http.StatusForbidden {
			t.Fatalf("listing keys without keys:write: %d %v", status, got)
		}
	}
	rows, err := shared.Pool().Query(t.Context(), `SELECT target, outcome, detail->>'code', actor_key IS NOT NULL FROM audit_events
		WHERE org_id = $1 AND action = 'access.denied'`, c.owner.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var target, outcome, code string
		var byKey bool
		if err := rows.Scan(&target, &outcome, &code, &byKey); err != nil {
			t.Fatal(err)
		}
		if target != "GET /v1/api_keys" || outcome != "denied" || code != "scope_missing" || !byKey {
			t.Errorf("the denial: %q %q %q, by a key %t", target, outcome, code, byKey)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("%d denials recorded for 3 refusals in a minute, want 1", n)
	}
}

// TestBrandKeysAreRefusedOrgWideFeeds checks a key limited to one brand
// cannot read events or manage webhook endpoints, which span every brand.
func TestBrandKeysAreRefusedOrgWideFeeds(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	brandID, err := id.Parse(id.Brand, c.brand)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := c.s.CreateOperatorAPIKey(t.Context(), c.owner, core.APIKeyInput{Name: "one brand", BrandID: &brandID})
	if err != nil {
		t.Fatal(err)
	}
	limited := *c
	limited.key = plain
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/events", ""},
		{http.MethodGet, "/v1/webhook_endpoints", ""},
		{http.MethodPost, "/v1/webhook_endpoints", `{"url":"https://hooks.example.com/x","enabled_events":["post.published"]}`},
	} {
		var body []byte
		ct := ""
		if rt.body != "" {
			body, ct = []byte(rt.body), "application/json"
		}
		if status, got := limited.do(rt.method, rt.path, ct, body, nil); status != http.StatusForbidden {
			t.Errorf("%s %s with a brand key: %d %v", rt.method, rt.path, status, got)
		}
	}
	if status, got := limited.do(http.MethodGet, "/v1/posts", "", nil, nil); status != http.StatusOK {
		t.Fatalf("its own brand's posts: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/events", "", nil, nil); status != http.StatusOK {
		t.Fatalf("a key for every brand: %d %v", status, got)
	}
}
