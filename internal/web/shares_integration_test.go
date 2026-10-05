// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// TestSharedReport checks an owner makes a link to a month's report, anyone
// opens it signed out (read-only, unindexed, accessible, linking nowhere in
// the dashboard), and withdrawing it ends it (ADR 0026).
func TestSharedReport(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	month := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	anon := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	brand := id.Format(id.Brand, d.brand.ID)
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/reports?brand="+brand+"&month="+month, nil)); !strings.Contains(rec.Body.String(), "Make a link") {
		t.Fatalf("the report page offers no share link:\n%s", rec.Body.String())
	}
	rec := post("/reports/shares", url.Values{"brand": {brand}, "month": {month}})
	link := regexp.MustCompile(`https://araldo\.test(/r/[A-Za-z0-9_-]+)`).FindStringSubmatch(rec.Body.String())
	share := regexp.MustCompile(`/reports/shares/([0-9a-f-]{36})/revoke`).FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || link == nil || share == nil {
		t.Fatalf("making a link: %d\n%s", rec.Code, rec.Body.String())
	}

	rec = anon(link[1])
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, d.brand.Name) || rec.Header().Get("X-Robots-Tag") == "" ||
		strings.Contains(body, `href="/posts/`) || strings.Contains(body, `href="/newsletters/`) || strings.Contains(body, "Sign out") {
		t.Fatalf("the shared report: %d %v\n%s", rec.Code, rec.Header(), body)
	}
	for _, issue := range a11yIssues(body, false) {
		t.Errorf("the shared report: %s", issue)
	}

	if rec := post("/reports/shares/"+share[1]+"/revoke", url.Values{"brand": {brand}, "month": {month}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("withdrawing: %d", rec.Code)
	}
	if rec := anon(link[1]); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Report not found") {
		t.Fatalf("a withdrawn link: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := anon("/r/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown link: %d", rec.Code)
	}
}
