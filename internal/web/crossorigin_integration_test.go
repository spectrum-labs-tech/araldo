// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// TestCrossSitePostsAreRefused checks that a form posted from another site
// is refused before it reaches a handler, signed in or not, while the same
// post from the dashboard itself, from a client that sends no browser
// headers, and a mail provider's one-click unsubscribe still get through.
func TestCrossSitePostsAreRefused(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	tests := []struct {
		name     string
		path     string
		fetch    string // Sec-Fetch-Site
		origin   string
		signedIn bool
		refused  bool
	}{
		{name: "sign-in from another site", path: "/login", fetch: "cross-site", refused: true},
		{name: "sign-in from another site, Origin only", path: "/login", origin: "https://evil.example", refused: true},
		{name: "sign-in from a sibling subdomain", path: "/login", fetch: "same-site", refused: true},
		{name: "single sign-on from another site", path: "/login/sso", fetch: "cross-site", refused: true},
		{name: "signed-in form from another site", path: "/onboarding", fetch: "cross-site", signedIn: true, refused: true},
		{name: "sign-in from the dashboard", path: "/login", fetch: "same-origin"},
		{name: "sign-in with no browser headers", path: "/login"},
		{name: "one-click unsubscribe from a mail provider", path: "/unsubscribe/nope", fetch: "cross-site"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "https://araldo.test"+tt.path,
				strings.NewReader(url.Values{"email": {"nobody@example.com"}, "password": {"wrong"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tt.fetch)
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			var rec *httptest.ResponseRecorder
			if tt.signedIn {
				rec = d.send(r)
			} else {
				rec = httptest.NewRecorder()
				d.srv.ServeHTTP(rec, r)
			}
			refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "sent from another site")
			if !tt.refused && (rec.Code == http.StatusForbidden || rec.Code >= 500) {
				t.Fatalf("%s: %d; want the handler's own answer\n%s", tt.path, rec.Code, rec.Body.String())
			}
			if refused != tt.refused {
				t.Fatalf("%s: %d, refused = %v, want %v\n%s", tt.path, rec.Code, refused, tt.refused, rec.Body.String())
			}
		})
	}
}

// TestOversizedFormsAreNotRead checks that a form post is read only up to
// MaxForm: valid credentials after a megabyte of padding are never seen.
func TestOversizedFormsAreNotRead(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	creds := "&email=" + url.QueryEscape(d.login.User.Email) + "&password=" + url.QueryEscape("correct horse battery")
	for _, tt := range []struct {
		name    string
		padding int
		signsIn bool
	}{
		{"within the limit", 1000, true},
		{"past the limit", web.MaxForm, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := "pad=" + strings.Repeat("x", tt.padding) + creds
			r := httptest.NewRequest(http.MethodPost, "https://araldo.test/login", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			d.srv.ServeHTTP(rec, r)
			if signedIn := rec.Code == http.StatusSeeOther; signedIn != tt.signsIn || rec.Code >= 500 {
				t.Fatalf("%d bytes of padding: %d, signed in = %v, want %v", tt.padding, rec.Code, signedIn, tt.signsIn)
			}
		})
	}
}
