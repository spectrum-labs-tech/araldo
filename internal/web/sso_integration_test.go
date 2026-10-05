// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/oidctest"
)

// TestSSOInTheDashboard drives single sign-on as a browser would (ADR
// 0033): the owner connects a provider and verifies a domain, someone signs
// in through it and joins, the sign-in is accepted only in the browser that
// started it, and once the org requires it a password session sees only
// the way in.
func TestSSOInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	idp := oidctest.New(t)
	d.s.SSOHTTP = idp.Client()
	domain := fmt.Sprintf("sso-%s.example", uuid.NewString()[:8])
	records := map[string][]string{}
	d.s.LookupTXT = func(_ context.Context, name string) ([]string, error) { return records[name], nil }
	csrf := d.login.Session.CSRFToken

	// do sends a request with a session cookie (none if ""), and an SSO
	// state cookie if given.
	do := func(method, path, session, state string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "araldo_session", Value: session})
		}
		if state != "" {
			r.AddCookie(&http.Cookie{Name: "araldo_sso", Value: state})
		}
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, r)
		return rec
	}
	cookie := func(rec *httptest.ResponseRecorder, name string) string {
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == name {
				return ck.Value
			}
		}
		return ""
	}
	owner := d.login.Token

	// The owner connects the provider and verifies a domain.
	rec := do(http.MethodPost, "/org/sso", owner, "", url.Values{"csrf": {csrf}, "issuer": {idp.Issuer()}, "client_id": {idp.ClientID},
		"client_secret": {idp.ClientSecret}, "default_role": {"editor"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("connecting: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/org/sso/domains", owner, "", url.Values{"csrf": {csrf}, "action": {"add"}, "domain": {domain}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("adding the domain: %d", rec.Code)
	}
	page := do(http.MethodGet, "/org/sso", owner, "", nil).Body.String()
	m := regexp.MustCompile(`araldo-verify=([A-Za-z0-9_-]+)`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, core.SSOVerifyPrefix+domain) || !strings.Contains(page, "https://araldo.test/login/sso/callback") {
		t.Fatalf("the settings page does not say what to publish:\n%s", page)
	}
	if rec := do(http.MethodPost, "/org/sso/domains", owner, "", url.Values{"csrf": {csrf}, "action": {"verify"}, "domain": {domain}}); rec.Code !=
		http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "No TXT record") {
		t.Fatalf("verifying before the record exists: %d", rec.Code)
	}
	records[core.SSOVerifyPrefix+domain] = []string{"araldo-verify=" + m[1]}
	if rec := do(http.MethodPost, "/org/sso/domains", owner, "", url.Values{"csrf": {csrf}, "action": {"verify"}, "domain": {domain}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("verifying: %d", rec.Code)
	}

	// Someone signs in through it, signed out: the form, a link to the
	// provider, and back.
	signIn := func(email string) (state, code string) {
		t.Helper()
		rec := do(http.MethodPost, "/login/sso", "", "", url.Values{"email": {email}, "next": {"/posts"}})
		link := regexp.MustCompile(`href="(` + regexp.QuoteMeta(idp.Issuer()) + `/authorize\?[^"]+)"`).FindStringSubmatch(rec.Body.String())
		if rec.Code != http.StatusOK || link == nil {
			t.Fatalf("starting: %d\n%s", rec.Code, rec.Body.String())
		}
		for _, issue := range a11yIssues(rec.Body.String(), false) {
			t.Errorf("the redirect page: %s", issue)
		}
		state, code, err := idp.Authorize(html.UnescapeString(link[1]), map[string]any{"email": email, "name": "Ada"})
		if err != nil || state != cookie(rec, "araldo_sso") {
			t.Fatalf("at the provider: %v", err)
		}
		return state, code
	}
	email := "ada@" + domain
	state, code := signIn(email)
	callback := "/login/sso/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)
	if rec := do(http.MethodGet, callback, "", "", nil); rec.Code != http.StatusBadRequest || cookie(rec, "araldo_session") != "" {
		t.Fatalf("the answer in a browser that did not start it: %d", rec.Code)
	}
	rec = do(http.MethodGet, callback, "", state, nil)
	ada := cookie(rec, "araldo_session")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts" || ada == "" {
		t.Fatalf("coming back: %d to %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if rec := do(http.MethodGet, "/posts", ada, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the new member's dashboard: %d", rec.Code)
	}
	u, err := d.s.UserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if mem, err := d.s.Member(ctx, d.owner, u.ID); err != nil || mem.Role != model.RoleEditor {
		t.Fatalf("joined as %+v, %v", mem, err)
	}
	if rec := do(http.MethodGet, "/login/sso/callback?error=access_denied&state=x", "", "x", nil); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "ask your administrator") {
		t.Fatalf("the provider refusing: %d", rec.Code)
	}

	// Requiring it: not from the owner's password session; from Ada's, once
	// she is an owner. Then the password session sees only the way in.
	if rec := do(http.MethodPost, "/org/sso/require", owner, "", url.Values{"csrf": {csrf}, "require": {"1"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("requiring it from a password session: %d", rec.Code)
	}
	if err := d.s.SetMemberRole(ctx, d.owner, d.login.Session, u.ID, model.RoleOwner); err != nil {
		t.Fatal(err)
	}
	adaSession, _, err := d.s.Session(ctx, ada)
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(http.MethodPost, "/org/sso/require", ada, "", url.Values{"csrf": {adaSession.CSRFToken}, "require": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("requiring it through SSO: %d\n%s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/posts", owner, "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Single sign-on required") {
		t.Fatalf("a password session once it is required: %d", rec.Code)
	}
	for _, issue := range a11yIssues(rec.Body.String(), true) {
		t.Errorf("the required page: %s", issue)
	}
	if rec := do(http.MethodGet, "/account", owner, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the account page once it is required: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/org/sso", ada, "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Stop requiring it") {
		t.Fatalf("the settings through SSO: %d", rec.Code)
	}
}
