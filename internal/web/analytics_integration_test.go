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

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestPerformanceShowsSignupsByPost(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Launch week, day one"},
		PublishAt: time.Now().Add(72 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	page := func() string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/performance", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /performance: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if got := page(); !strings.Contains(got, "Connect the brand's web analytics below") || !strings.Contains(got, `action="/performance/analytics"`) {
		t.Fatalf("before connecting, the page should offer the form:\n%s", got)
	}

	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "provider": {"sandbox"}, "brand": {id.Format(id.Brand, d.brand.ID)},
		"field_site": {"shop.example"}, "field_tags": {"bluesky/social/launch/" + id.Format(id.Post, p.ID)}, "goals": {"Signup, Newsletter"}}
	r := httptest.NewRequest(http.MethodPost, "/performance/analytics", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("connect: %d\n%s", rec.Code, rec.Body)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.CollectAnalytics(ctx); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(page(), ">Launch week, day one</a>") || time.Now().After(deadline) {
			break
		}
	}
	got := page()
	for _, want := range []string{">Launch week, day one</a>", `<span class="tag">Signup `, `<span class="tag">Newsletter `, "came without Araldo's tags"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the page should show %q:\n%s", want, got)
		}
	}
}
