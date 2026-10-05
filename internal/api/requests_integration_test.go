// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestRequestsAreLogged checks each authenticated request reaches the
// org's request log with its route, outcome and credential, in its mode,
// and that a filter by outcome finds it (ADR 0032).
func TestRequestsAreLogged(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	if status, got := c.do(http.MethodGet, "/v1/brands", "", nil, nil); status != http.StatusOK {
		t.Fatalf("brands: %d %v", status, got)
	}
	if status, _ := c.do(http.MethodGet, "/v1/posts/post_nope", "", nil, nil); status != http.StatusNotFound && status != http.StatusBadRequest {
		t.Fatalf("a missing post: %d", status)
	}
	var logged []model.APIRequest
	for deadline := time.Now().Add(10 * time.Second); len(logged) < 2; time.Sleep(50 * time.Millisecond) {
		var err error
		if logged, _, err = c.s.APIRequests(t.Context(), c.owner, store.RequestFilter{}, store.Page{}); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("logged %d requests, want 2: %+v", len(logged), logged)
		}
	}
	missing, brands := logged[0], logged[1] // newest first
	if brands.Route != "GET /v1/brands" || brands.Status != http.StatusOK || brands.KeyID == nil || brands.Livemode {
		t.Fatalf("the brands request: %+v", brands)
	}
	if missing.Route != "GET /v1/posts/{id}" || missing.Path != "/v1/posts/post_nope" || missing.Status < 400 || missing.ErrorCode == "" {
		t.Fatalf("the failed request: %+v", missing)
	}
	refused, _, err := c.s.APIRequests(t.Context(), c.owner, store.RequestFilter{StatusClass: 4}, store.Page{})
	if err != nil || len(refused) != 1 || refused[0].ID != missing.ID {
		t.Fatalf("4xx only: %+v, %v", refused, err)
	}
	live := c.owner
	live.Livemode = true
	if inLive, _, err := c.s.APIRequests(t.Context(), live, store.RequestFilter{}, store.Page{}); err != nil || len(inLive) != 0 {
		t.Fatalf("live mode shows test requests: %+v, %v", inLive, err)
	}
}
