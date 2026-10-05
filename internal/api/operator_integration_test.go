// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// TestOperatorAPI drives the operator API end to end: operator keys reach
// only it, and it creates, finds, changes, invites into and deletes orgs,
// whose own keys feel the status it sets (ADR 0031).
func TestOperatorAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	plain, _, err := c.s.CreateOperatorKey(t.Context(), core.InstallOperator("test"), "billing "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	op := *c
	op.key = plain
	jsonBody := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Each credential reaches only its own API.
	if status, got := c.do(http.MethodGet, "/v1/operator/orgs", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("an org key on the operator API: %d %v", status, got)
	}
	if status, got := op.do(http.MethodGet, "/v1/brands", "", nil, nil); status != http.StatusUnauthorized || !strings.Contains(got["detail"].(string), "/v1/operator/") {
		t.Fatalf("an operator key on the org API: %d %v", status, got)
	}

	ref := "cus_" + uuid.NewString()
	status, created := op.do(http.MethodPost, "/v1/operator/orgs", "application/json", jsonBody(map[string]any{
		"name": "Hosted " + ref[:12], "owner_email": "first-" + uuid.NewString()[:8] + "@example.com", "external_ref": ref,
		"limits": map[string]any{"brands": 1, "posts_per_month": nil}}), nil)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, created)
	}
	inv, _ := created["owner_invitation"].(map[string]any)
	limits, _ := created["limits"].(map[string]any)
	if created["external_ref"] != ref || created["status"] != "active" || inv["role"] != "owner" || !strings.Contains(inv["url"].(string), "/invite/") ||
		limits["brands"] != float64(1) || limits["posts_per_month"] != nil {
		t.Fatalf("created %v", created)
	}
	orgID := created["id"].(string)
	if status, got := op.do(http.MethodPost, "/v1/operator/orgs", "application/json", jsonBody(map[string]any{
		"name": "Again", "owner_email": "again@example.com", "external_ref": ref}), nil); status != http.StatusConflict || got["code"] != "external_ref_taken" {
		t.Fatalf("a second org with the reference: %d %v", status, got)
	}
	status, found := op.do(http.MethodGet, "/v1/operator/orgs?external_ref="+url.QueryEscape(ref), "", nil, nil)
	if data, _ := found["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != orgID {
		t.Fatalf("by reference: %d %v", status, found)
	}
	if status, got := op.do(http.MethodPost, "/v1/operator/orgs/"+orgID+"/invitations", "application/json",
		jsonBody(map[string]any{"email": "second@example.com", "role": "admin"}), nil); status != http.StatusCreated || got["url"] == nil {
		t.Fatalf("invite: %d %v", status, got)
	}
	if status, got := op.do(http.MethodGet, "/v1/operator/orgs/"+orgID+"/usage?month=2026-13", "", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("a month that is not one: %d %v", status, got)
	}
	if status, got := op.do(http.MethodGet, "/v1/operator/orgs/"+orgID+"/usage?month=2026-10", "", nil, nil); status != http.StatusOK ||
		got["object"] != "usage" || got["members"] != float64(2) || got["period_start"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("usage: %d %v", status, got)
	}

	// Suspending the client's org refuses its key until it is active again.
	mine := "/v1/operator/orgs/" + id.Format(id.Org, c.owner.OrgID)
	if status, got := op.do(http.MethodPost, mine, "application/json", jsonBody(map[string]any{"status": "suspended", "status_note": "Unpaid"}), nil); status != http.StatusOK ||
		got["status"] != "suspended" {
		t.Fatalf("suspend: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/brands", "", nil, nil); status != http.StatusForbidden || got["code"] != "org_suspended" {
		t.Fatalf("a suspended org's key: %d %v", status, got)
	}
	if status, got := op.do(http.MethodPost, mine, "application/json", jsonBody(map[string]any{"status": "read_only"}), nil); status != http.StatusOK {
		t.Fatalf("read-only: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/brands", "", nil, nil); status != http.StatusOK {
		t.Fatalf("a read-only org's key reading: %d %v", status, got)
	}
	if status, got := c.do(http.MethodPost, "/v1/brands", "application/json", jsonBody(map[string]any{"name": "No", "timezone": "UTC"}), nil); status != http.StatusForbidden ||
		got["code"] != "org_read_only" {
		t.Fatalf("a read-only org's key writing: %d %v", status, got)
	}
	if status, got := op.do(http.MethodPost, mine, "application/json", jsonBody(map[string]any{"status": "active"}), nil); status != http.StatusOK {
		t.Fatalf("reactivate: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/brands", "", nil, nil); status != http.StatusOK {
		t.Fatalf("an active org's key: %d %v", status, got)
	}

	// Deleting needs the org's name.
	if status, got := op.do(http.MethodDelete, "/v1/operator/orgs/"+orgID+"?confirm=nope", "", nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("delete with the wrong name: %d %v", status, got)
	}
	if status, got := op.do(http.MethodDelete, "/v1/operator/orgs/"+orgID+"?confirm="+url.QueryEscape("Hosted "+ref[:12]), "", nil, nil); status != http.StatusOK ||
		got["deleted"] != true {
		t.Fatalf("delete: %d %v", status, got)
	}
	if status, got := op.do(http.MethodGet, "/v1/operator/orgs/"+orgID, "", nil, nil); status != http.StatusNotFound {
		t.Fatalf("a deleted org: %d %v", status, got)
	}
}
