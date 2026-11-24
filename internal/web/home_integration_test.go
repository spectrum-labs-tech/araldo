// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestOverviewPage checks the overview: an org with nothing yet shows
// getting started and that nothing needs anyone; one with work shows what
// waits for approval, an expiring key, what is coming up in the next
// week, and its brands, each linking where it is dealt with.
func TestOverviewPage(t *testing.T) {
	t.Parallel()
	home := func(d *dash) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /: %d", rec.Code)
		}
		page := rec.Body.String()
		for _, issue := range a11yIssues(page, true) {
			t.Errorf("/: %s", issue)
		}
		return page
	}

	fresh := home(newDash(t))
	for _, want := range []string{"Get started", "Nothing needs you.", "Nothing scheduled in the next 7 days", "Turn on two-factor authentication",
		`href="/posts/new">Publish your first post</a>`} {
		if !strings.Contains(fresh, want) {
			t.Errorf("a new org's overview lacks %q", want)
		}
	}

	d := newDash(t)
	ctx := t.Context()
	if _, err := d.s.ConnectMailAccount(ctx, d.owner, core.MailAccountInput{BrandID: d.brand.ID, Provider: email.Sandbox, FromName: "News",
		FromEmail: "news@araldo.dev", DefaultAudiences: []string{"list-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.SetEmailTheme(ctx, d.owner, d.brand.ID, core.EmailThemeInput{PostalAddress: "1 Main St"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.UpdateBrand(ctx, d.owner, d.brand.ID, core.BrandInput{Name: d.brand.Name, Timezone: d.brand.Timezone,
		ApprovalPolicy: model.ApprovalAll}); err != nil {
		t.Fatal(err)
	}
	p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Shipped the overview"},
		PublishAt: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)})
	if err != nil || p.Status != model.PostPendingApproval {
		t.Fatalf("a post waiting for approval: %v, %+v", err, p)
	}
	is, err := d.s.CreateIssue(ctx, d.owner, core.IssueInput{BrandID: d.brand.ID, Subject: "This week at Araldo", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if is, err = d.s.ScheduleIssue(ctx, d.owner, is.ID, time.Now().Add(72*time.Hour)); err != nil || is.Status != model.IssuePendingApproval {
		t.Fatalf("an issue waiting for approval: %v, %+v", err, is)
	}
	soon := time.Now().Add(3 * 24 * time.Hour)
	_, k, err := d.s.CreateAPIKey(ctx, d.owner, d.login.Session, core.APIKeyInput{Name: "ci soon gone", Expires: &soon})
	if err != nil {
		t.Fatal(err)
	}

	page := home(d)
	postLink := `href="/posts/` + id.Format(id.Post, p.ID) + `"`
	issueLink := `href="/newsletters/` + id.Format(id.Issue, is.ID) + `">This week at Araldo</a>`
	for _, want := range []string{"Waiting for your approval", postLink, issueLink, "API keys expiring",
		`href="/keys/` + id.Format(id.APIKey, k.ID) + `">ci soon gone</a>`, `href="/brands/` + id.Format(id.Brand, d.brand.ID) + `">`,
		`href="/newsletters/new">New issue</a>`, "Posts published"} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %s", want)
		}
	}
	if strings.Contains(page, "Nothing needs you.") || strings.Contains(page, "Nothing scheduled in the next 7 days") {
		t.Error("the overview says nothing is waiting or coming")
	}
	// Coming up lists both, the post (in two days) before the issue (in three).
	upcoming := page[strings.Index(page, `id="upcoming-heading"`):]
	if i, j := strings.Index(upcoming, postLink), strings.Index(upcoming, issueLink); i < 0 || j < 0 || i > j {
		t.Errorf("coming up: the post at %d, the issue at %d", i, j)
	}
}
