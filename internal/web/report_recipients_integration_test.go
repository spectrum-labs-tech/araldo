// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// TestReportRecipientsInTheDashboard checks admins add and remove the
// addresses a brand's monthly report goes to, from the Reports page (ADR
// 0026).
func TestReportRecipientsInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	brand := id.Format(id.Brand, d.brand.ID)
	post := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		form.Set("brand", brand)
		r := httptest.NewRequest(http.MethodPost, "/reports/recipients", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	page := func() string {
		return d.send(httptest.NewRequest(http.MethodGet, "/reports?brand="+brand, nil)).Body.String()
	}

	if rec := post(url.Values{"email": {"client@example.com"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("adding: %d", rec.Code)
	}
	if !strings.Contains(page(), "client@example.com") {
		t.Fatal("the page does not list the address")
	}
	if rec := post(url.Values{"email": {"client@example.com"}}); rec.Code != http.StatusSeeOther ||
		!strings.Contains(rec.Header().Get("Location"), "already+gets") {
		t.Fatalf("adding it twice: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := post(url.Values{"email": {"client@example.com"}, "action": {"remove"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("removing: %d", rec.Code)
	}
	if strings.Contains(page(), "client@example.com</li>") || strings.Contains(page(), `value="client@example.com"`) {
		t.Fatal("still listed after removing")
	}
}
