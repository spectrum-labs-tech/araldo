// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

// offersSource is a newsletter template a product fills from its own data
// (ADR 0024 phase 2).
var offersSource = tmpl.Source{
	Variables: json.RawMessage(`{"type":"object","required":["offers"],"properties":{"offers":{"type":"array","minItems":1,"items":{"type":"object",` +
		`"required":["title","url"],"properties":{"title":{"type":"string"},"url":{"type":"string"}}}}}}`),
	Examples:    []json.RawMessage{json.RawMessage(`{"offers":[{"title":"Example deal","url":"https://araldo.dev/d/1"}]}`)},
	Subject:     `{{len .offers}} {{plural (len .offers) "deal" "deals"}} this week`,
	PreviewText: `Starting with {{(index .offers 0).title}}`,
	Body:        "# This week's deals\n{{range .offers}}\n- [{{md .title}}]({{.url}}){{end}}",
}

func TestNewsletterTemplates(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	newTemplate := func(key string, k model.TemplateKind, src tmpl.Source) (*model.Template, error) {
		tpl, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: key, Kind: k, Source: src})
		return tpl, err
	}

	// What a newsletter template must be, and a post template must not.
	for _, c := range []struct {
		name string
		kind model.TemplateKind
		src  tmpl.Source
		want string
	}{
		{"no subject", model.TemplateNewsletter, tmpl.Source{Body: "Hello"}, "subject_missing"},
		{"a body per platform", model.TemplateNewsletter, tmpl.Source{Subject: "s", Body: "b",
			Overrides: map[platform.Provider]string{platform.Bluesky: "b"}}, "overrides_not_allowed"},
		{"an example that renders a broken body", model.TemplateNewsletter, tmpl.Source{Subject: "s", Body: "[Go]({{.url}}){.button}",
			Examples: []json.RawMessage{json.RawMessage(`{"url":"ftp://x"}`)}}, "body_invalid"},
		{"an example missing a field", model.TemplateNewsletter, tmpl.Source{Subject: "{{.s}}", Body: "b",
			Examples: []json.RawMessage{json.RawMessage(`{}`)}}, "variable_missing"},
		{"a post template with a subject", model.TemplatePost, tmpl.Source{Subject: "s", Body: "b"}, "subject_not_allowed"},
		{"an unknown kind", "email", tmpl.Source{Body: "b"}, "kind_invalid"},
	} {
		if _, err := newTemplate("bad-"+uuid.NewString()[:8], c.kind, c.src); !hasProblem(err, c.want) {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}

	offers, err := newTemplate("weekly-offers", model.TemplateNewsletter, offersSource)
	if err != nil {
		t.Fatal(err)
	}
	if offers.Kind != model.TemplateNewsletter {
		t.Fatalf("kind %q", offers.Kind)
	}
	post, err := newTemplate("featured", "", tmpl.Source{Body: "Featured: {{.name}}"})
	if err != nil || post.Kind != model.TemplatePost {
		t.Fatalf("a template with no kind is a post template: %+v, %v", post, err)
	}

	// Saved templates preview as an issue, with their example or given data.
	got, err := w.s.PreviewNewsletterTemplate(ctx, w.owner, offersSource, nil, "UTC")
	if err != nil || got.Subject != "1 deal this week" || !strings.Contains(got.Body, "[Example deal](https://araldo.dev/d/1)") {
		t.Fatalf("preview with the example: %+v, %v", got, err)
	}

	// An issue from the template: the text is rendered, the template, version
	// and data recorded, and data is plain text in the Markdown.
	data := json.RawMessage(`{"offers":[{"title":"*Big* sale","url":"https://araldo.dev/d/2"},{"title":"Optic","url":"https://araldo.dev/d/3"}]}`)
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Template: "weekly-offers", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if is.Subject != "2 deals this week" || is.PreviewText != "Starting with *Big* sale" || !strings.Contains(is.Body, `[\*Big\* sale](https://araldo.dev/d/2)`) ||
		is.TemplateID == nil || *is.TemplateID != offers.ID || is.TemplateVersion == nil || *is.TemplateVersion != 1 || string(is.Data) == "" {
		t.Fatalf("issue from a template: %+v", is)
	}
	if v := core.ViewIssue(is); v.Template != id.Format(id.Template, offers.ID) || v.TemplateVersion == nil {
		t.Errorf("the view names the template: %+v", v)
	}
	rd, err := w.s.PreviewIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Template: "weekly-offers@1", Data: data})
	if err != nil || !strings.Contains(rd.HTML, "*Big* sale</a>") {
		t.Fatalf("previewing from a template: %v", err)
	}

	// What an issue from a template refuses.
	for _, c := range []struct {
		name string
		in   core.IssueInput
		want string
	}{
		{"a subject beside the template", core.IssueInput{Template: "weekly-offers", Data: data, Subject: "Mine"}, "content_conflict"},
		{"data the variables refuse", core.IssueInput{Template: "weekly-offers", Data: json.RawMessage(`{"offers":[]}`)}, "data_invalid"},
		{"a post template", core.IssueInput{Template: "featured", Data: json.RawMessage(`{"name":"x"}`)}, "template_kind"},
		{"no such template", core.IssueInput{Template: "nope"}, "template_missing"},
	} {
		c.in.BrandID = w.brand.ID
		if _, err := w.s.CreateIssue(ctx, w.owner, c.in); code(err) != c.want && !hasProblem(err, c.want) {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	// A post refuses a newsletter template.
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Template: "weekly-offers", Data: data, PublishAt: "next_slot"}); code(err) != "template_kind" {
		t.Errorf("a post from a newsletter template: %v", err)
	}

	// Saving the text unchanged keeps the template; editing it by hand
	// makes it the editor's.
	if is, err = w.s.UpdateIssue(ctx, w.owner, is.ID, core.IssueInput{Subject: is.Subject, PreviewText: is.PreviewText, Body: is.Body}); err != nil || is.TemplateID == nil {
		t.Fatalf("saved unchanged: %v, template %v", err, is.TemplateID)
	}
	if is, err = w.s.UpdateIssue(ctx, w.owner, is.ID, core.IssueInput{Subject: is.Subject, Body: is.Body + "\n\nP.S."}); err != nil ||
		is.TemplateID != nil || is.TemplateVersion != nil || is.Data != nil {
		t.Fatalf("edited by hand: %v, %+v", err, is)
	}
	// Rendering it again from the template, with new data, takes it back.
	if is, err = w.s.UpdateIssue(ctx, w.owner, is.ID, core.IssueInput{Template: "weekly-offers", Data: data}); err != nil || is.TemplateID == nil ||
		strings.Contains(is.Body, "P.S.") {
		t.Fatalf("rendered again: %v, %+v", err, is)
	}
}

// TestNewsletterTemplateApproval checks an issue follows its template's
// approval rule only while its text is as the template rendered it.
func TestNewsletterTemplateApproval(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	setTheme(t, w)
	connectSandboxMail(t, w, "news@araldo.dev", nil)
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalEditorsAndKey}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "weekly-offers", Kind: model.TemplateNewsletter,
		Approval: model.TemplateApprovalNotRequired, Source: offersSource}); err != nil {
		t.Fatal(err)
	}
	editorUser, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, err := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"offers":[{"title":"Optic","url":"https://araldo.dev/d/3"}]}`)
	sendAt := time.Now().Add(48 * time.Hour)

	is, err := w.s.CreateIssue(ctx, editor, core.IssueInput{BrandID: w.brand.ID, Template: "weekly-offers", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if is, err = w.s.ScheduleIssue(ctx, editor, is.ID, sendAt); err != nil || is.Status != model.IssueScheduled {
		t.Fatalf("as rendered, the template's rule lets it through: %v, %s", err, is.Status)
	}

	edited, err := w.s.CreateIssue(ctx, editor, core.IssueInput{BrandID: w.brand.ID, Template: "weekly-offers", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if edited, err = w.s.UpdateIssue(ctx, editor, edited.ID, core.IssueInput{Subject: "My own subject", Body: edited.Body}); err != nil {
		t.Fatal(err)
	}
	if edited, err = w.s.ScheduleIssue(ctx, editor, edited.ID, sendAt); err != nil || edited.Status != model.IssuePendingApproval {
		t.Fatalf("edited by hand, the brand's policy applies: %v, %s", err, edited.Status)
	}
}
