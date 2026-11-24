// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

// TestNewsletterTemplatesInTheDashboard checks a newsletter template is
// made on its own form, previews as the email its example makes, keeps
// its kind, and that an issue made from it says so.
func TestNewsletterTemplatesInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	page := get("/templates/new?kind=newsletter")
	if !strings.Contains(page, `href="/templates/new?kind=newsletter" aria-current="true"`) || !strings.Contains(page, `name="subject"`) ||
		!strings.Contains(page, `name="kind" value="newsletter"`) || strings.Contains(page, `name="override_`) ||
		!strings.Contains(page, `formaction="/templates/newsletter_preview"`) {
		t.Fatalf("the newsletter template form:\n%s", page)
	}
	if page := get("/templates/new"); !strings.Contains(page, `href="/templates/new" aria-current="true"`) || strings.Contains(page, `name="subject"`) {
		t.Fatal("the post template form is the default, with no subject")
	}

	form := url.Values{"brand": {id.Format(id.Brand, d.brand.ID)}, "kind": {"newsletter"}, "key": {"weekly"}, "name": {"Weekly"},
		"subject": {"Week of {{.week}}"}, "preview_text": {"What shipped"}, "body": {"# Week of {{md .week}}\n\nHello"},
		"variables": {`{"type":"object","required":["week"],"properties":{"week":{"type":"string"}}}`}, "example": {`{"week":"May 4"}`}}

	// The preview is the email the example makes.
	prev := post("/templates/newsletter_preview", form)
	if prev.Code != http.StatusOK || !strings.Contains(prev.Body.String(), "Week of May 4") || !strings.Contains(prev.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("preview: %d\n%s", prev.Code, prev.Body)
	}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("subject", "")
	if rec := post("/templates/newsletter_preview", bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "needs a subject") {
		t.Fatalf("a preview with no subject: %d\n%s", rec.Code, rec.Body)
	}
	// Shown again with its problems, the form keeps its kind and text.
	if rec := post("/templates", bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `name="kind" value="newsletter"`) ||
		!strings.Contains(rec.Body.String(), "What shipped") {
		t.Fatalf("creating with no subject: %d\n%s", rec.Code, rec.Body)
	}

	rec := post("/templates", form)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/templates/tmpl_") {
		t.Fatalf("create: %d\n%s", rec.Code, rec.Body)
	}
	tplPath := strings.SplitN(rec.Header().Get("Location"), "?", 2)[0]
	page = get(tplPath)
	if !strings.Contains(page, `value="Week of {{.week}}"`) || strings.Contains(page, "Use in a post") || !strings.Contains(page, `formtarget="tpl-preview"`) {
		t.Fatalf("the template's page:\n%s", page)
	}
	if page := get("/templates"); !strings.Contains(page, "Newsletter issues") {
		t.Fatal("the list says what each template is for")
	}
	// Saving without changing the text makes no new version.
	if rec := post(tplPath, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving unchanged: %d\n%s", rec.Code, rec.Body)
	}
	tid, err := id.Parse(id.Template, strings.TrimPrefix(tplPath, "/templates/"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, _, err := d.s.Template(t.Context(), d.owner, tid, 0)
	if err != nil || tpl.Kind != model.TemplateNewsletter || tpl.LatestVersion != 1 {
		t.Fatalf("after saving unchanged: %+v, %v", tpl, err)
	}

	is, err := d.s.CreateIssue(t.Context(), d.owner, core.IssueInput{BrandID: d.brand.ID, Template: "weekly", Data: json.RawMessage(`{"week":"May 11"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if page := get("/newsletters/" + id.Format(id.Issue, is.ID)); !strings.Contains(page, `Made from the template <a href="`+tplPath+`"><code>weekly</code></a>, version 1.`) ||
		!strings.Contains(page, `value="Week of May 11"`) {
		t.Fatalf("the issue page names its template:\n%s", page)
	}
}

// TestTemplatesListFilters checks the templates list: sorted by brand,
// then key, and narrowed by brand, kind and a search of key and name,
// with a way back to all of them.
func TestTemplatesListFilters(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	// A brand named to sort before the dashboard's own ("Araldo").
	other, err := d.s.CreateBrand(ctx, d.owner, core.BrandInput{Name: "AAA Open B00KS " + id.New().String()[:6]})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(brand uuid.UUID, key, name string, kind model.TemplateKind) {
		t.Helper()
		src := tmpl.Source{Body: "Shipped {{.version}}", Examples: []json.RawMessage{json.RawMessage(`{"version":"1.0"}`)}}
		if kind == model.TemplateNewsletter {
			src = tmpl.Source{Subject: "Week of {{.week}}", Body: "# Week of {{md .week}}", Examples: []json.RawMessage{json.RawMessage(`{"week":"May 4"}`)}}
		}
		if _, _, err := d.s.CreateTemplate(ctx, d.owner, core.TemplateInput{BrandID: brand, Key: key, Name: name, Kind: kind, Source: src}); err != nil {
			t.Fatal(err)
		}
	}
	mk(d.brand.ID, "zeta-release", "Release notes", model.TemplatePost)
	mk(d.brand.ID, "alpha-weekly", "Weekly digest", model.TemplateNewsletter)
	mk(other.ID, "omega-launch", "Launch", model.TemplatePost)

	list := func(query string) []string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/templates"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /templates%s: %d", query, rec.Code)
		}
		var keys []string
		for _, m := range regexp.MustCompile(`<a href="/templates/tmpl_[0-9a-z]+"><code>([^<]+)</code>`).FindAllStringSubmatch(rec.Body.String(), -1) {
			keys = append(keys, m[1])
		}
		return keys
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"omega-launch", "alpha-weekly", "zeta-release"}}, // the other brand first, then by key
		{"?brand=" + id.Format(id.Brand, d.brand.ID), []string{"alpha-weekly", "zeta-release"}},
		{"?brand=" + other.Slug, []string{"omega-launch"}},
		{"?kind=newsletter", []string{"alpha-weekly"}},
		{"?kind=post&brand=" + d.brand.Slug, []string{"zeta-release"}},
		{"?q=DIGEST", []string{"alpha-weekly"}}, // by name, ignoring case
		{"?q=launch", []string{"omega-launch"}}, // by key
		{"?kind=bogus", []string{"omega-launch", "alpha-weekly", "zeta-release"}},
		{"?q=nothing-like-it", nil},
	}
	for _, tt := range tests {
		if got := list(tt.query); !slices.Equal(got, tt.want) {
			t.Errorf("%q: %v, want %v", tt.query, got, tt.want)
		}
	}
	page := d.send(httptest.NewRequest(http.MethodGet, "/templates?q=nothing-like-it", nil)).Body.String()
	if !strings.Contains(page, "No templates match.") || !strings.Contains(page, `<a class="btn" href="/templates">Clear</a>`) {
		t.Errorf("no match:\n%s", page)
	}
	for _, issue := range a11yIssues(page, true) {
		t.Errorf("/templates (filtered): %s", issue)
	}
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/templates?brand=brand_nope", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown brand: %d", rec.Code)
	}
}
