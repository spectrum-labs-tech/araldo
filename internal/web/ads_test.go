// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import "testing"

func TestMoney(t *testing.T) {
	t.Parallel()
	tests := []struct {
		minor    int64
		currency string
		want     string
	}{
		{123456, "USD", "1234.56 USD"},
		{5, "EUR", "0.05 EUR"},
		{0, "USD", "0.00 USD"},
		{-250, "USD", "-2.50 USD"},
		{1500, "JPY", "1500 JPY"},
	}
	for _, tt := range tests {
		if got := money(tt.minor, tt.currency); got != tt.want {
			t.Errorf("money(%d, %s) = %q, want %q", tt.minor, tt.currency, got, tt.want)
		}
	}
}

func TestTagLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, source, campaign, content string
		want, problem                        string
	}{
		{"a plain page", "https://openb00ks.example/learn/openai-batch-api-migration", "reddit", "Alpha launch!", "LocalLLaMA ad 1",
			"https://openb00ks.example/learn/openai-batch-api-migration?utm_campaign=alpha-launch&utm_content=localllama-ad-1&utm_medium=paid&utm_source=reddit", ""},
		{"keeps the page's own query and replaces old tags", "https://openb00ks.example/pricing?plan=batch&utm_source=old&utm_content=x", "Reddit", "spring", "",
			"https://openb00ks.example/pricing?plan=batch&utm_campaign=spring&utm_medium=paid&utm_source=reddit", ""},
		{"no source", "https://example.com/", "", "c1", "", "https://example.com/?utm_campaign=c1&utm_medium=paid&utm_source=ads", ""},
		{"not an address", "openb00ks.example/pricing", "reddit", "c1", "", "", "Give the landing page's full address, starting with https://."},
		{"another scheme", "javascript:alert(1)", "reddit", "c1", "", "", "Give the landing page's full address, starting with https://."},
		{"no campaign", "https://example.com/", "reddit", " !! ", "", "", "Name the campaign."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, problem := tagLink(tt.url, tt.source, tt.campaign, tt.content)
			if got != tt.want || problem != tt.problem {
				t.Fatalf("tagLink = %q, %q; want %q, %q", got, problem, tt.want, tt.problem)
			}
		})
	}
}
