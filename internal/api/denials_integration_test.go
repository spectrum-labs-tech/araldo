// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"
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
