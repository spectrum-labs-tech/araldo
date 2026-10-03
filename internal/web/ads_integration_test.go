// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

func TestAdsPage(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	page := func() string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/ads", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ads: %d", rec.Code)
		}
		return rec.Body.String()
	}
	got := page()
	if !strings.Contains(got, "No ad accounts are connected.") || !strings.Contains(got, `action="/ads/accounts"`) {
		t.Fatalf("an owner should see the connect form:\n%s", got)
	}
	// The guide's first step is not done yet.
	if !strings.Contains(got, `<aside class="guide" aria-label="Guide">`) || strings.Contains(got, `<li class="done">`) {
		t.Fatalf("the guide should start with nothing done:\n%s", got)
	}
	connect := func(name string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {d.login.Session.CSRFToken}, "network": {"sandbox"}, "brand": {id.Format(id.Brand, d.brand.ID)},
			"field_name": {name}, "field_currency": {"EUR"}}
		r := httptest.NewRequest(http.MethodPost, "/ads/accounts", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	if rec := connect("Otium EU"); rec.Code != http.StatusSeeOther {
		t.Fatalf("connect: %d\n%s", rec.Code, rec.Body)
	}
	// The same account again shows why, on the page.
	if rec := connect("Otium EU"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "already connected") {
		t.Fatalf("connecting twice: %d\n%s", rec.Code, rec.Body)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.CollectAds(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := page(); strings.Contains(got, "Launch") || time.Now().After(deadline) {
			break
		}
	}
	got = page()
	if strings.Count(got, `<li class="done">`) != 2 {
		t.Fatalf("connecting and reading should tick the guide's first two steps:\n%s", got)
	}
	if !strings.Contains(got, "Retargeting") || !strings.Contains(got, " EUR</td>") || !strings.Contains(got, "Otium EU") {
		t.Fatalf("the page should show both campaigns in euros:\n%s", got)
	}
	if strings.Count(got, `<td class="text-right">—</td>`) != 2 {
		t.Fatalf("without web analytics no campaign has a cost per signup:\n%s", got)
	}

	// Web analytics credit the Launch campaign's tagged links.
	src, err := d.s.ConnectAnalyticsSource(t.Context(), d.owner, core.AnalyticsSourceInput{BrandID: d.brand.ID, Provider: analytics.Sandbox,
		Goals: []string{"Signup"}, Fields: map[string]string{"site": "otium.example", "tags": "sandbox/paid/launch/ad-1"}})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.CollectAnalytics(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got, err := d.s.AnalyticsSource(t.Context(), d.owner, src.ID); err != nil || got.ReadAt != nil || time.Now().After(deadline) {
			break
		}
	}
	if got := page(); strings.Count(got, `<td class="text-right">—</td>`) != 1 {
		t.Fatalf("Launch should have a cost per signup and Retargeting none:\n%s", got)
	}
}

func TestAdsGuideBuildsTaggedLinks(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	get := func(q url.Values) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/ads?"+q.Encode(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ads: %d", rec.Code)
		}
		return rec.Body.String()
	}
	got := get(url.Values{"tag_url": {"https://getotium.ai/pricing"}, "tag_source": {"reddit"}, "tag_campaign": {"Alpha launch"}})
	if want := "https://getotium.ai/pricing?utm_campaign=alpha-launch&amp;utm_medium=paid&amp;utm_source=reddit"; !strings.Contains(got, want) {
		t.Fatalf("the builder should show %s:\n%s", want, got)
	}
	if got := get(url.Values{"tag_url": {"getotium.ai"}, "tag_campaign": {"x"}}); !strings.Contains(got, "full address, starting with https://") {
		t.Fatalf("a bad landing page should say why:\n%s", got)
	}
	// Pages without a guide keep their single column.
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/posts", nil)); strings.Contains(rec.Body.String(), `class="with-guide"`) {
		t.Fatal("a page without a guide should not get the guide column")
	}
}
