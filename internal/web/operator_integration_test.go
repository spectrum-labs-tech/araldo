// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// TestHostedDashboard checks what an install hosting orgs for others shows
// (ADR 0031): where to sign up, the way to billing with a hand-off, usage
// against limits, and a read-only banner or a suspended notice.
func TestHostedDashboard(t *testing.T) {
	t.Parallel()
	key := []byte(strings.Repeat("k", 32))
	d := newDashWith(t, func(cfg *core.Config) {
		cfg.SignupURL = "https://araldo.example/signup"
		cfg.BillingURL, cfg.BillingLinkKey = "https://billing.araldo.example/", key
	})
	ctx := t.Context()
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		return d.send(httptest.NewRequest(http.MethodGet, path, nil))
	}
	accessible := func(path string, rec *httptest.ResponseRecorder, signedIn bool) {
		t.Helper()
		for _, issue := range a11yIssues(rec.Body.String(), signedIn) {
			t.Errorf("%s: %s", path, issue)
		}
	}

	// Signed out, and signed in without making orgs.
	srv, err := web.New(d.s, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if !strings.Contains(rec.Body.String(), `href="https://araldo.example/signup"`) {
		t.Fatalf("the sign-in page has no sign-up link:\n%s", rec.Body.String())
	}
	accessible("/login", rec, false)
	if rec := get("/onboarding"); !strings.Contains(rec.Body.String(), "created when you sign up") || strings.Contains(rec.Body.String(), `action="/onboarding"`) {
		t.Fatalf("onboarding offers to create an org:\n%s", rec.Body.String())
	}
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "name": {"Mine"}}
	r := httptest.NewRequest(http.MethodPost, "/onboarding", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(r); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("creating an org anyway: %d", rec.Code)
	}

	// Billing: the owner's link, and the hand-off it carries.
	if rec := get("/"); !strings.Contains(rec.Body.String(), `href="/org/billing"`) {
		t.Fatal("the owner has no billing link")
	}
	rec = get("/org/billing")
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "https://billing.araldo.example/?token=") {
		t.Fatalf("billing: %d to %q", rec.Code, loc)
	}
	u, _ := url.Parse(loc)
	if c, err := core.VerifyBillingHandoff(key, u.Query().Get("token"), time.Now()); err != nil || c.Org != id.Format(id.Org, d.owner.OrgID) {
		t.Fatalf("the hand-off: %+v, %v", c, err)
	}

	// Limits and status, as the operator sets them.
	in, _, err := d.s.InOrg(ctx, core.InstallOperator("test"), d.owner.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	readOnly, note := model.OrgReadOnly, "Your card was declined."
	if _, err := d.s.ChangeOrg(ctx, in, core.OrgChange{Limits: &model.OrgLimits{Brands: &one}, Status: &readOnly, StatusNote: &note}); err != nil {
		t.Fatal(err)
	}
	rec = get("/org")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "Usage and limits") || !strings.Contains(body, "read-only") ||
		!strings.Contains(body, note) {
		t.Fatalf("the org page of a limited, read-only org: %d\n%s", rec.Code, body)
	}
	accessible("/org", rec, true)
	if rec := get("/channels/apps"); rec.Code != http.StatusOK {
		t.Fatalf("a read-only org's apps page: %d", rec.Code)
	}
	form = url.Values{"csrf": {d.login.Session.CSRFToken}, "name": {"Another"}, "timezone": {"UTC"}}
	r = httptest.NewRequest(http.MethodPost, "/brands", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(r); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("adding a brand to a read-only org: %d", rec.Code)
	}

	suspended := model.OrgSuspended
	if _, err := d.s.ChangeOrg(ctx, in, core.OrgChange{Status: &suspended}); err != nil {
		t.Fatal(err)
	}
	rec = get("/posts")
	if body := rec.Body.String(); rec.Code != http.StatusForbidden || !strings.Contains(body, "This org is suspended") ||
		!strings.Contains(body, note) || !strings.Contains(body, `href="/org/billing"`) {
		t.Fatalf("a suspended org's posts: %d\n%s", rec.Code, body)
	}
	accessible("/posts (suspended)", rec, true)
	if rec := get("/account"); rec.Code != http.StatusOK {
		t.Fatalf("one's own account in a suspended org: %d", rec.Code)
	}
	if rec := get("/org/billing"); rec.Code != http.StatusSeeOther {
		t.Fatalf("billing from a suspended org: %d", rec.Code)
	}
}
