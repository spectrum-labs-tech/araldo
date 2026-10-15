// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"
)

// TestNotificationsOverTheAPI reads and marks a person's notifications
// with their user token: each device sign-in is one (ADR 0034). API keys
// are refused.
func TestNotificationsOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	tokenFor(t, c)       // a first sign-in
	as := tokenFor(t, c) // a second sign-in: two notices
	get := func(q string) (int, map[string]any) {
		t.Helper()
		return c.do(http.MethodGet, "/v1/notifications"+q, "", nil, as)
	}

	if status, got := c.do(http.MethodGet, "/v1/notifications", "", nil, nil); status != http.StatusForbidden {
		t.Fatalf("with an API key: %d %v", status, got)
	}
	status, got := get("")
	data, _ := got["data"].([]any)
	if status != http.StatusOK || len(data) != 2 {
		t.Fatalf("listing: %d %v", status, got)
	}
	n := data[0].(map[string]any)
	if n["object"] != "notification" || n["type"] != "account.cli_signed_in" || n["org"] != nil || n["read_at"] != nil ||
		n["link"] != "https://araldo.test/account" {
		t.Fatalf("a notification: %v", n)
	}

	// Paging.
	status, got = get("?limit=1")
	page, _ := got["data"].([]any)
	if status != http.StatusOK || len(page) != 1 || got["has_more"] != true || page[0].(map[string]any)["id"] != n["id"] {
		t.Fatalf("the first page: %d %v", status, got)
	}
	status, got = get("?limit=1&starting_after=" + n["id"].(string))
	page, _ = got["data"].([]any)
	if status != http.StatusOK || len(page) != 1 || got["has_more"] != false || page[0].(map[string]any)["id"] != data[1].(map[string]any)["id"] {
		t.Fatalf("the next page: %d %v", status, got)
	}

	// Reading.
	if status, got := c.do(http.MethodPost, "/v1/notifications/"+n["id"].(string)+"/read", "", nil, as); status != http.StatusOK || got["read_at"] == nil {
		t.Fatalf("marking one read: %d %v", status, got)
	}
	if _, got := get("?unread=true"); len(got["data"].([]any)) != 1 {
		t.Fatalf("unread after reading one: %v", got)
	}
	if status, got := c.do(http.MethodPost, "/v1/notifications/read_all", "", nil, as); status != http.StatusOK || got["unread"] != float64(0) {
		t.Fatalf("reading all: %d %v", status, got)
	}
	if _, got := get("?unread=true"); len(got["data"].([]any)) != 0 {
		t.Fatalf("unread after reading all: %v", got)
	}
	if status, _ := get("?unread=maybe"); status != http.StatusBadRequest {
		t.Fatalf("a bad unread: %d", status)
	}
}

// TestNotificationsPageBothWays checks the notifications list pages as
// every other list does, ending_before included.
func TestNotificationsPageBothWays(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	as := tokenFor(t, c)
	// Signing a device in notifies the person; sign in twice more.
	tokenFor(t, c)
	tokenFor(t, c)
	status, all := c.do(http.MethodGet, "/v1/notifications?limit=100", "", nil, as)
	data, _ := all["data"].([]any)
	if status != http.StatusOK || len(data) < 3 {
		t.Fatalf("all: %d %v", status, all)
	}
	newest := data[0].(map[string]any)["id"].(string)
	second := data[1].(map[string]any)["id"].(string)
	status, older := c.do(http.MethodGet, "/v1/notifications?limit=1&starting_after="+newest, "", nil, as)
	if got, _ := older["data"].([]any); status != http.StatusOK || len(got) != 1 || got[0].(map[string]any)["id"] != second {
		t.Fatalf("starting_after the newest: %d %v", status, older)
	}
	status, newer := c.do(http.MethodGet, "/v1/notifications?ending_before="+second, "", nil, as)
	if got, _ := newer["data"].([]any); status != http.StatusOK || len(got) != 1 || got[0].(map[string]any)["id"] != newest {
		t.Fatalf("ending_before the second: %d %v", status, newer)
	}
}
