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

	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// TestNotificationsInTheDashboard checks the inbox: the header's count,
// opening a notification follows its link and marks it read, and the
// settings save what is ticked (ADR 0034).
func TestNotificationsInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	csrf := d.login.Session.CSRFToken
	if err := d.s.ChangePassword(ctx, d.login.Session, "correct horse battery", "a whole new password"); err != nil {
		t.Fatal(err)
	}

	page := d.send(httptest.NewRequest(http.MethodGet, "/notifications", nil)).Body.String()
	m := regexp.MustCompile(`href="/notifications/([0-9a-f-]{36})"[^>]*>(?:<span class="sr-only">Unread: </span>)?Your Araldo password was changed`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `1<span class="sr-only"> unread</span>`) {
		t.Fatalf("the inbox:\n%s", page)
	}
	rec := d.send(httptest.NewRequest(http.MethodGet, "/notifications/"+m[1], nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account" {
		t.Fatalf("opening it: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if page := d.send(httptest.NewRequest(http.MethodGet, "/notifications", nil)).Body.String(); strings.Contains(page, "sr-only\"> unread") ||
		strings.Contains(page, "Unread: ") {
		t.Fatal("still unread after opening it")
	}
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/notifications/00000000-0000-0000-0000-000000000000", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("someone else's or no notification: %d", rec.Code)
	}

	// Settings: only what is ticked stays on.
	form := url.Values{"csrf": {csrf}, "in_app:" + core.NotifyPostApproval: {"1"}, "email:" + core.NotifyChannelReauth: {"1"}}
	r := httptest.NewRequest(http.MethodPost, "/notifications/settings", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the settings: %d", rec.Code)
	}
	choices, err := d.s.NotificationChoices(ctx, d.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range choices {
		wantInApp, wantEmail := c.Key == core.NotifyPostApproval, c.Key == core.NotifyChannelReauth
		if c.InApp != wantInApp || c.Email != wantEmail {
			t.Errorf("%s: in app %v email %v", c.Key, c.InApp, c.Email)
		}
	}
}
