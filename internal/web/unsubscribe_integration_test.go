// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// TestUnsubscribeLink checks a notification email's unsubscribe link:
// opening it only asks, posting it as a mail app does (RFC 8058) turns that
// type's email off without a session, and a tampered link does nothing
// (ADR 0034).
func TestUnsubscribeLink(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	link, err := d.s.UnsubscribeURL(ctx, d.login.User.ID, d.owner.OrgID, core.NotifyPostApproval)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(link, "https://araldo.test")
	emailOn := func() bool {
		choices, err := d.s.NotificationChoices(ctx, d.owner)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range choices {
			if c.Key == core.NotifyPostApproval {
				return c.Email
			}
		}
		t.Fatal("no approval choice")
		return false
	}

	rec := httptest.NewRecorder()
	d.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Stop these emails") || !emailOn() {
		t.Fatalf("opening the link: %d, email still on %v", rec.Code, emailOn())
	}
	for _, issue := range a11yIssues(rec.Body.String(), false) {
		t.Errorf("the unsubscribe page: %s", issue)
	}

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("List-Unsubscribe=One-Click"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	d.srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no longer emails you") || emailOn() {
		t.Fatalf("one-click unsubscribe: %d, email still on %v", rec.Code, emailOn())
	}

	rec = httptest.NewRecorder()
	d.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path[:len(path)-3]+"abc", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a tampered link: %d", rec.Code)
	}
}
