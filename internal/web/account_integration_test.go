// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/authn"
)

// TestAccountPageKeepsItsSections checks that a form's problem or result
// shows the whole account page, not one emptied of its passkeys, devices
// and recovery codes left.
func TestAccountPageKeepsItsSections(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	secret, _, err := d.s.BeginTOTP(ctx, d.login.Session)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.ConfirmTOTP(ctx, d.login.Session, authn.TOTPCode(key, authn.TOTPStep(time.Now()))); err != nil {
		t.Fatal(err)
	}
	const left = "10 recovery codes left"
	if page := d.send(httptest.NewRequest(http.MethodGet, "/account", nil)).Body.String(); !strings.Contains(page, left) {
		t.Fatalf("the account page lacks %q:\n%s", left, page)
	}
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "current_password": {"correct horse battery"},
		"new_password": {"a new long password"}, "confirm_password": {"a different one"}}
	r := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := d.send(r)
	if page := rec.Body.String(); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "do not match") || !strings.Contains(page, left) {
		t.Fatalf("a mismatched password: %d\n%s", rec.Code, page)
	}
}
