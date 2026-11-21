// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
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
