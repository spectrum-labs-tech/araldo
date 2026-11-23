// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// TestBrandPages checks a brand's three pages: the overview links to what
// the brand has, settings and the email theme save on their own pages
// without touching each other, the sidebar lists the brand's pages under
// Brands, and the theme previews a sample issue with unsaved values.
func TestBrandPages(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	base := "/brands/" + id.Format(id.Brand, d.brand.ID)
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}

	// The overview: what the brand has, and that the theme lacks an address.
	if _, err := d.s.CreateIssue(ctx, d.owner, core.IssueInput{BrandID: d.brand.ID, Subject: "Spring issue", Body: "Hello"}); err != nil {
		t.Fatal(err)
	}
	page := get(base)
	for _, want := range []string{"<h1>" + d.brand.Name + "</h1>", "Spring issue", `href="` + base + `/email-theme">email theme</a> has no postal address`,
		`href="/media?brand=` + id.Format(id.Brand, d.brand.ID) + `"`, `href="/channels/new?brand=`} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %s", want)
		}
	}
	// The sidebar: Brands is the current section, the brand's pages under
	// it with this one current; the phone's row of the same links too.
	for _, want := range []string{`<a href="/brands" aria-current="true">Brands</a>`, `<a href="` + base + `" class="active" aria-current="page">Overview</a>`,
		`<a href="` + base + `/settings" >Settings</a>`, `class="choices md:hidden"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the brand's navigation lacks %s", want)
		}
	}
	if list := get("/brands"); !strings.Contains(list, `<a href="/brands" class="active" aria-current="page">Brands</a>`) || strings.Contains(list, "side-sub") {
		t.Error("the brands list marks Brands current, with no brand's pages under it")
	}

	// Settings save on their page and come back to it; the theme is kept.
	if _, err := d.s.SetEmailTheme(ctx, d.owner, d.brand.ID, core.EmailThemeInput{Accent: "#0f5132", PostalAddress: "1 Main St"}); err != nil {
		t.Fatal(err)
	}
	settings := url.Values{"name": {d.brand.Name}, "slug": {d.brand.Slug}, "timezone": {"America/Chicago"}, "approval_policy": {"none"}, "slots": {"mon 09:00"}}
	if rec := post(base, settings); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base+"/settings" {
		t.Fatalf("saving settings: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	b, err := d.s.Brand(ctx, d.owner, d.brand.ID)
	if err != nil || b.Timezone != "America/Chicago" || b.EmailTheme.Accent != "#0f5132" || b.EmailTheme.PostalAddress != "1 Main St" {
		t.Fatalf("after saving settings: %+v, %v", b, err)
	}
	bad := url.Values{}
	for k, v := range settings {
		bad[k] = v
	}
	bad.Set("slots", "someday 25:00")
	if rec := post(base, bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "someday 25:00") ||
		!strings.Contains(rec.Body.String(), "<h1>Settings</h1>") || !strings.Contains(rec.Body.String(), `aria-current="page">Settings</a>`) {
		t.Fatalf("settings with a bad slot: %d\n%s", rec.Code, rec.Body)
	}
	if page := get(base + "/settings"); strings.Contains(page, `name="postal_address"`) || !strings.Contains(page, `name="slots"`) {
		t.Error("the settings page has the settings, not the theme")
	}

	// The theme saves on its page; the settings are kept.
	page = get(base + "/email-theme")
	if !strings.Contains(page, `name="postal_address"`) || strings.Contains(page, `name="slots"`) ||
		!strings.Contains(page, `src="`+base+`/email-theme/preview"`) || !strings.Contains(page, `formaction="`+base+`/email-theme/preview"`) {
		t.Fatalf("the theme page:\n%s", page)
	}
	if rec := post(base+"/email_theme", url.Values{"accent": {"#1d4ed8"}, "postal_address": {"2 Side St"}, "footer": {"You asked for it."}}); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != base+"/email-theme" {
		t.Fatalf("saving the theme: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if b, err = d.s.Brand(ctx, d.owner, d.brand.ID); err != nil || b.Timezone != "America/Chicago" || b.EmailTheme.PostalAddress != "2 Side St" {
		t.Fatalf("after saving the theme: %+v, %v", b, err)
	}
	if rec := post(base+"/email_theme", url.Values{"accent": {"#fde047"}, "postal_address": {"3 Typed St"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "3 Typed St") || !strings.Contains(rec.Body.String(), "<h1>Email theme</h1>") {
		t.Fatalf("a theme too light to read: %d\n%s", rec.Code, rec.Body)
	}

	// The preview: the sample issue in the unsaved theme, sandboxed.
	rec := post(base+"/email-theme/preview", url.Values{"accent": {"#7c2d12"}, "postal_address": {"9 Preview Rd"}, "footer": {"A footer to see."}})
	html := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("preview: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	for _, want := range []string{"#7c2d12", "9 Preview Rd", "A footer to see.", "What shipped this week", "Read the release notes", "data:image/svg+xml"} {
		if !strings.Contains(html, want) {
			t.Errorf("the preview lacks %s", want)
		}
	}
	if rec := post(base+"/email-theme/preview", url.Values{"accent": {"#fde047"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "dark enough") {
		t.Fatalf("previewing a light accent: %d\n%s", rec.Code, rec.Body)
	}
	if b, err = d.s.Brand(ctx, d.owner, d.brand.ID); err != nil || b.EmailTheme.PostalAddress != "2 Side St" {
		t.Fatalf("a preview saved the theme: %+v, %v", b.EmailTheme, err)
	}
	// The frame's first content is the saved theme.
	if rec := d.send(httptest.NewRequest(http.MethodGet, base+"/email-theme/preview", nil)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2 Side St") {
		t.Fatalf("the saved theme's preview: %d", rec.Code)
	}

	// Another org's brand is not found on any of its pages.
	other := newDash(t)
	for _, path := range []string{"", "/settings", "/email-theme", "/email-theme/preview"} {
		if rec := other.send(httptest.NewRequest(http.MethodGet, base+path, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("another org's %s%s: %d", base, path, rec.Code)
		}
	}
}
