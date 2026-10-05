// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/webauthntest"
)

// TestPasskeysInTheDashboard drives the passkey endpoints as the page's
// script does, with a software authenticator: add one from the account
// page, sign in with it alone, use it as a password sign-in's second
// factor, and remove it (ADR 0007).
func TestPasskeysInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	device := &webauthntest.Authenticator{RPID: "araldo.test", Origin: "https://araldo.test"}
	csrf := d.login.Session.CSRFToken
	// call posts JSON with the given cookie and CSRF header.
	call := func(path, cookie, csrf string, body any) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "araldo_session", Value: cookie})
		}
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	optionsOf := func(out map[string]any) []byte {
		t.Helper()
		raw, err := json.Marshal(out["options"])
		if err != nil || out["token"] == nil {
			t.Fatalf("no options: %v", out)
		}
		return raw
	}

	// Adding one needs the session's CSRF token, and JSON.
	if rec, _ := call("/account/passkeys/options", d.login.Token, "", map[string]any{}); rec.Code != http.StatusForbidden {
		t.Fatalf("options without the CSRF token: %d", rec.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/login/passkey/options", strings.NewReader("a=b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := httptest.NewRecorder(); func() int { d.srv.ServeHTTP(rec, r); return rec.Code }() != http.StatusBadRequest {
		t.Fatal("a form post (as another site could send) was accepted")
	}
	rec, out := call("/account/passkeys/options", d.login.Token, csrf, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("options: %d %v", rec.Code, out)
	}
	resp, err := device.Create(optionsOf(out))
	if err != nil {
		t.Fatal(err)
	}
	if rec, out = call("/account/passkeys", d.login.Token, csrf, map[string]any{"token": out["token"], "name": "Work laptop",
		"response": json.RawMessage(resp)}); rec.Code != http.StatusOK || !strings.HasPrefix(out["next"].(string), "/account") {
		t.Fatalf("adding: %d %v", rec.Code, out)
	}
	page := d.send(httptest.NewRequest(http.MethodGet, "/account", nil)).Body.String()
	if !strings.Contains(page, "Work laptop") {
		t.Fatalf("the account page does not list the passkey:\n%s", page)
	}
	for _, issue := range a11yIssues(page, true) {
		t.Errorf("/account: %s", issue)
	}

	// Signing in with it alone, signed out.
	_, out = call("/login/passkey/options", "", "", map[string]any{})
	if resp, err = device.Get(optionsOf(out)); err != nil {
		t.Fatal(err)
	}
	rec, out = call("/login/passkey", "", "", map[string]any{"token": out["token"], "response": json.RawMessage(resp), "next": "/posts"})
	cookie := ""
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "araldo_session" {
			cookie = ck.Value
		}
	}
	if rec.Code != http.StatusOK || out["next"] != "/posts" || cookie == "" {
		t.Fatalf("signing in with the passkey: %d %v", rec.Code, out)
	}
	if rec, _ := call("/login/passkey", "", "", map[string]any{"token": "nope", "response": json.RawMessage(resp)}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a made-up ceremony: %d", rec.Code)
	}

	// A password sign-in then asks for it.
	form := url.Values{"email": {d.login.User.Email}, "password": {"correct horse battery"}}
	lr := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lrec := httptest.NewRecorder()
	d.srv.ServeHTTP(lrec, lr)
	pending := ""
	for _, ck := range lrec.Result().Cookies() {
		if ck.Name == "araldo_session" {
			pending = ck.Value
		}
	}
	if !strings.HasPrefix(lrec.Header().Get("Location"), "/login/mfa") || pending == "" {
		t.Fatalf("a password sign-in with a passkey: %d to %q", lrec.Code, lrec.Header().Get("Location"))
	}
	mr := httptest.NewRequest(http.MethodGet, "/login/mfa", nil)
	mr.AddCookie(&http.Cookie{Name: "araldo_session", Value: pending})
	mrec := httptest.NewRecorder()
	d.srv.ServeHTTP(mrec, mr)
	if body := mrec.Body.String(); !strings.Contains(body, "Use your passkey") || strings.Contains(body, `name="code"`) {
		t.Fatalf("the second-factor page for a passkey only:\n%s", body)
	}
	for _, issue := range a11yIssues(mrec.Body.String(), false) {
		t.Errorf("/login/mfa: %s", issue)
	}
	_, out = call("/login/mfa/passkey/options", pending, "", map[string]any{})
	if resp, err = device.Get(optionsOf(out)); err != nil {
		t.Fatal(err)
	}
	if rec, out = call("/login/mfa/passkey", pending, "", map[string]any{"token": out["token"], "response": json.RawMessage(resp)}); rec.Code != http.StatusOK {
		t.Fatalf("the passkey as the second factor: %d %v", rec.Code, out)
	}

	// Removing it, from the account page.
	id := regexp.MustCompile(`/account/passkeys/([0-9a-f-]{36})/delete`).FindStringSubmatch(page)
	if id == nil {
		t.Fatal("no passkey to remove")
	}
	del := httptest.NewRequest(http.MethodPost, "/account/passkeys/"+id[1]+"/delete", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
	del.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(del); rec.Code != http.StatusSeeOther || strings.Contains(d.send(httptest.NewRequest(http.MethodGet, "/account", nil)).Body.String(),
		"Work laptop") {
		t.Fatalf("removing: %d", rec.Code)
	}
}
