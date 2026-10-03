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
	if got := page(); !strings.Contains(got, "No ad accounts are connected.") || !strings.Contains(got, `action="/ads/accounts"`) {
		t.Fatalf("an owner should see the connect form:\n%s", got)
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
	got := page()
	if !strings.Contains(got, "Retargeting") || !strings.Contains(got, " EUR</td>") || !strings.Contains(got, "Otium EU") {
		t.Fatalf("the page should show both campaigns in euros:\n%s", got)
	}
}
