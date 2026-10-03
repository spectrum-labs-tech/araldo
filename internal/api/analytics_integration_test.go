// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestAnalyticsOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, providers := c.json(http.MethodGet, "/v1/analytics_providers", nil)
	if data, _ := providers["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["provider"] != "sandbox" {
		t.Fatalf("providers: %d %v", status, providers)
	}
	body := map[string]any{"brand": c.brand, "provider": "sandbox", "goals": []string{"Signup"},
		"fields": map[string]string{"site": "api.example", "tags": "mastodon/social/launch/post_x"}}
	status, src := c.json(http.MethodPost, "/v1/analytics_sources", body)
	if status != http.StatusCreated || src["object"] != "analytics_source" || src["credentials"] != nil {
		t.Fatalf("connect: %d %v", status, src)
	}
	sid := src["id"].(string)
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := c.s.CollectAnalytics(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, got := c.json(http.MethodGet, "/v1/analytics_sources/"+sid, nil); got["read_at"] != nil || time.Now().After(deadline) {
			break
		}
	}
	status, sum := c.json(http.MethodGet, "/v1/analytics/summary?group_by=source", nil)
	rows, _ := sum["data"].([]any)
	if status != http.StatusOK || sum["object"] != "analytics_summary" || len(rows) != 1 || sum["untagged"].(float64) <= 0 {
		t.Fatalf("summary: %d %v", status, sum)
	}
	if row := rows[0].(map[string]any); row["id"] != "mastodon" || row["conversions"].(float64) <= 0 {
		t.Fatalf("source row %v", row)
	}
	if status, got := c.json(http.MethodGet, "/v1/analytics/summary?group_by=hour", nil); status != http.StatusUnprocessableEntity || got["code"] != "group_by_invalid" {
		t.Fatalf("a bad grouping: %d %v", status, got)
	}
	if status, got := c.json(http.MethodDelete, "/v1/analytics_sources/"+sid, nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("disconnect: %d %v", status, got)
	}
}
