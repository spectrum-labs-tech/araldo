// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestAdsOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, nets := c.json(http.MethodGet, "/v1/ad_networks", nil)
	data, _ := nets["data"].([]any)
	if status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["network"] != "sandbox" {
		t.Fatalf("ad networks: %d %v", status, nets)
	}

	body := map[string]any{"brand": c.brand, "network": "sandbox", "fields": map[string]string{"name": "Otium"}}
	if status, got := c.json(http.MethodPost, "/v1/ad_accounts", body); status != http.StatusForbidden || got["code"] != "scope_missing" {
		t.Fatalf("a full-access key connecting: %d %v", status, got)
	}
	ads := c.keyWith("ads:read", "ads:write")
	status, acct := ads.json(http.MethodPost, "/v1/ad_accounts", body)
	if status != http.StatusCreated || acct["object"] != "ad_account" || acct["currency"] != "USD" || acct["credentials"] != nil {
		t.Fatalf("connect: %d %v", status, acct)
	}
	aid := acct["id"].(string)
	if status, got := ads.json(http.MethodPost, "/v1/ad_accounts", body); status != http.StatusConflict || got["code"] != "ad_account_connected" {
		t.Fatalf("connecting again: %d %v", status, got)
	}

	// Readers claim due accounts in every org, and other tests run them too:
	// read until this account has been read, by whichever reader.
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := c.s.CollectAds(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, got := c.json(http.MethodGet, "/v1/ad_accounts/"+aid, nil); got["read_at"] != nil || time.Now().After(deadline) {
			break
		}
	}
	status, sum := c.json(http.MethodGet, "/v1/ads/summary?group_by=account&account="+aid, nil)
	rows, _ := sum["data"].([]any)
	if status != http.StatusOK || sum["object"] != "ads_summary" || len(rows) != 1 {
		t.Fatalf("summary: %d %v", status, sum)
	}
	row := rows[0].(map[string]any)
	spend, clicks, cpc := row["spend"].(float64), row["clicks"].(float64), row["cost_per_click"].(float64)
	if row["id"] != aid || row["currency"] != "USD" || spend <= 0 || clicks <= 0 || cpc != float64(int64((spend+clicks/2)/clicks)) {
		t.Fatalf("account row %v", row)
	}
	status, days := c.json(http.MethodGet, "/v1/ads/summary?group_by=day&since=2020-01-01&until=2020-01-07", nil)
	if rows, _ := days["data"].([]any); status != http.StatusOK || len(rows) != 0 || days["since"] != "2020-01-01" {
		t.Fatalf("an empty window: %d %v", status, days)
	}
	for query, code := range map[string]string{
		"?since=yesterday":                     "parameter_invalid",
		"?group_by=hour":                       "group_by_invalid",
		"?since=2026-02-01&until=2026-01-01":   "window_invalid",
		"?account=chan_01j9x3k2v7e8f9g0h1j2k3": "parameter_invalid",
	} {
		if status, got := c.json(http.MethodGet, "/v1/ads/summary"+query, nil); got["code"] != code {
			t.Errorf("%s: %d %v, want %s", query, status, got, code)
		}
	}

	if status, got := c.json(http.MethodDelete, "/v1/ad_accounts/"+aid, nil); status != http.StatusForbidden {
		t.Fatalf("a full-access key disconnecting: %d %v", status, got)
	}
	if status, got := ads.json(http.MethodDelete, "/v1/ad_accounts/"+aid, nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("disconnect: %d %v", status, got)
	}
	if status, _ := c.json(http.MethodGet, "/v1/ad_accounts/"+aid, nil); status != http.StatusNotFound {
		t.Fatalf("a disconnected account: %d", status)
	}
}
