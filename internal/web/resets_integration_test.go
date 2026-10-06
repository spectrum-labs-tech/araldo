// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// mailbox is a mail sender that hands each message to a channel.
type mailbox chan smtpmail.Message

func (m mailbox) Send(_ context.Context, msg smtpmail.Message) error {
	m <- msg
	return nil
}

// TestPasswordResetInTheDashboard drives a reset as a browser would: ask
// for a link, follow it, choose a password, and find the link spent and
// the old session ended (ADR 0034).
func TestPasswordResetInTheDashboard(t *testing.T) {
	t.Parallel()
	box := make(mailbox, 4)
	d := newDashWith(t, func(cfg *core.Config) { cfg.Mail = box })
	email := d.login.User.Email
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, r)
		return rec
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if body := get("/login").Body.String(); !strings.Contains(body, `href="/login/forgot"`) {
		t.Fatalf("the sign-in page has no reset link:\n%s", body)
	}
	for _, path := range []string{"/login/forgot"} {
		for _, issue := range a11yIssues(get(path).Body.String(), false) {
			t.Errorf("%s: %s", path, issue)
		}
	}
	rec := post("/login/forgot", url.Values{"email": {email}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Check your email") {
		t.Fatalf("asking for a link: %d", rec.Code)
	}
	var msg smtpmail.Message
	select {
	case msg = <-box:
	case <-time.After(10 * time.Second):
		t.Fatal("no reset email was sent")
	}
	m := regexp.MustCompile(`https://araldo\.test(/login/reset/[A-Za-z0-9_-]+)`).FindStringSubmatch(msg.Text)
	if msg.To != email || m == nil {
		t.Fatalf("the email: to %q\n%s", msg.To, msg.Text)
	}
	link := m[1]

	rec = get(link)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="password"`) || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("the link: %d", rec.Code)
	}
	for _, issue := range a11yIssues(rec.Body.String(), false) {
		t.Errorf("the reset page: %s", issue)
	}
	if rec := post(link, url.Values{"password": {"a whole new password"}, "confirm_password": {"another one entirely"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("passwords that differ: %d", rec.Code)
	}
	rec = post(link, url.Values{"password": {"a whole new password"}, "confirm_password": {"a whole new password"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?notice=") {
		t.Fatalf("setting the password: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(link); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Ask for a new link") {
		t.Fatalf("the spent link: %d", rec.Code)
	}
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/posts", nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("the old session after the reset: %d", rec.Code)
	}
}

// TestPasswordResetWithoutMailInTheDashboard checks a server that sends no
// email keeps the old advice and refuses the form.
func TestPasswordResetWithoutMailInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	rec := httptest.NewRecorder()
	d.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if body := rec.Body.String(); strings.Contains(body, `href="/login/forgot"`) || !strings.Contains(body, "reset-password") {
		t.Fatalf("the sign-in page without email:\n%s", body)
	}
	r := httptest.NewRequest(http.MethodPost, "/login/forgot", strings.NewReader("email=a%40b.example"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	d.srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("asking for a link: %d", rec.Code)
	}
}
