// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDeviceSignInOverTheAPI drives the CLI's side of the device sign-in
// over HTTP (the person's approval is the dashboard's, here the service's),
// then uses the token as the person, and signs it out.
func TestDeviceSignInOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	ctx := t.Context()
	anon := map[string]string{"Authorization": ""}

	status, start := c.do(http.MethodPost, "/v1/auth/device", "application/json", []byte(`{"device_name":"araldo CLI on laptop"}`), anon)
	userCode, _ := start["user_code"].(string)
	deviceCode, _ := start["device_code"].(string)
	if status != http.StatusOK || userCode == "" || deviceCode == "" || !strings.HasSuffix(start["verification_uri"].(string), "/device") {
		t.Fatalf("starting: %d %v", status, start)
	}
	poll := func() (int, map[string]any) {
		return c.do(http.MethodPost, "/v1/auth/device/token", "application/json", []byte(`{"device_code":"`+deviceCode+`"}`), anon)
	}
	if status, got := poll(); status != http.StatusBadRequest || got["code"] != "authorization_pending" {
		t.Fatalf("polling before approval: %d %v", status, got)
	}

	u, err := c.s.User(ctx, *c.owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	login, err := c.s.Login(ctx, u.Email, "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.s.DecideDevice(ctx, login.Session, userCode, true); err != nil {
		t.Fatal(err)
	}
	c.s.Now = func() time.Time { return time.Now().Add(time.Minute) } // past the polling interval
	status, issued := poll()
	c.s.Now = time.Now
	token, _ := issued["access_token"].(string)
	if status != http.StatusOK || !strings.HasPrefix(token, "ald_user_") || issued["token_type"] != "bearer" {
		t.Fatalf("the token: %d %v", status, issued)
	}

	// The token is the person: their role, in the only org they belong to.
	as := map[string]string{"Authorization": "Bearer " + token}
	status, me := c.do(http.MethodGet, "/v1/me", "", nil, as)
	user, _ := me["user"].(map[string]any)
	if status != http.StatusOK || user["email"] != u.Email || me["role"] != "owner" || me["livemode"] != false || me["api_key"] != nil {
		t.Fatalf("/v1/me with the token: %d %v", status, me)
	}
	if status, got := c.do(http.MethodGet, "/v1/brands", "", nil, as); status != http.StatusOK {
		t.Fatalf("listing brands with the token: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/me", "", nil, map[string]string{"Authorization": "Bearer " + token, "Araldo-Org": "Not an org of theirs"}); status != http.StatusNotFound || got["code"] != "org_unknown" {
		t.Fatalf("naming an org they are not in: %d %v", status, got)
	}
	// Creating a key needs sudo mode, which only the dashboard has.
	b := []byte(`{"name":"from a token"}`)
	if status, _ := c.do(http.MethodPost, "/v1/api_keys", "application/json", b, as); status != http.StatusForbidden {
		t.Fatalf("creating a key with the token: %d", status)
	}

	// Signing out revokes it; an API key cannot sign itself out this way.
	if status, got := c.do(http.MethodDelete, "/v1/auth/token", "", nil, nil); status != http.StatusForbidden {
		t.Fatalf("an API key signing out: %d %v", status, got)
	}
	if status, got := c.do(http.MethodDelete, "/v1/auth/token", "", nil, as); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("signing out: %d %v", status, got)
	}
	if status, got := c.do(http.MethodGet, "/v1/me", "", nil, as); status != http.StatusUnauthorized || got["code"] != "user_token_invalid" {
		t.Fatalf("after signing out: %d %v", status, got)
	}
}
