// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	contract "github.com/spectrum-labs-tech/araldo/api"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Posts render as the contract says, including a next_slot post that has
// no time until it is approved (ADR 0022).
func TestPostViewsMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["Post"].Value
	at := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)
	by := at.Add(core.DefaultPublishWindow)
	post := func(status model.PostStatus, at, by, slot *time.Time, target model.TargetStatus) *model.Post {
		return &model.Post{ID: uuid.New(), BrandID: uuid.New(), Status: status, Content: &model.Content{Body: "x"}, PublishAt: at, PublishBy: by,
			SlotAt: slot, ApprovalNeeded: status == model.PostPendingApproval,
			Targets: []model.Target{{ID: uuid.New(), Status: target, NextAttemptAt: at, PublishBy: by, Parts: []string{"x"}}}}
	}
	tests := []struct {
		name     string
		post     *model.Post
		wantAt   any
		wantSlot bool
	}{
		{"waiting for a slot", post(model.PostPendingApproval, nil, nil, nil, model.TargetHeld), nil, true},
		{"waiting for a slot before a deadline", post(model.PostPendingApproval, nil, &by, nil, model.TargetHeld), nil, true},
		{"holding a slot", post(model.PostScheduled, &at, &by, &at, model.TargetQueued), "2026-10-05T15:00:00Z", true},
		{"at a time", post(model.PostScheduled, &at, &by, nil, model.TargetQueued), "2026-10-05T15:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(core.ViewPost(tt.post))
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(v); err != nil {
				t.Fatalf("does not match the contract: %v\n%s", err, raw)
			}
			if at, present := v["publish_at"]; !present || at != tt.wantAt || v["slot"] != tt.wantSlot {
				t.Fatalf("publish_at %v (present %t), slot %v; want %v, %t", at, present, v["slot"], tt.wantAt, tt.wantSlot)
			}
		})
	}
}

// Newsletter objects render as the contract says, nulls included: a draft
// has no send time, a disconnected account's delivery no account, and an
// audience a provider does not size no size.
func TestNewsletterViewsMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)
	acct := uuid.New()
	logo := uuid.New()
	draft := &model.Issue{ID: uuid.New(), BrandID: uuid.New(), Subject: "x", Body: "x", Status: model.IssueDraft,
		Deliveries: []model.IssueDelivery{{ID: uuid.New(), Provider: "sandbox", AccountName: "Gone", Status: model.DeliveryDraft,
			Audiences: []model.Audience{{ID: "segment-1", Name: "Engaged", Kind: "segment", Size: -1}}}}}
	sent := &model.Issue{ID: uuid.New(), BrandID: uuid.New(), Subject: "x", Body: "x", Status: model.IssueSent, SendAt: &at,
		ApprovalNeeded: true, ReviewedAt: &at, ReviewedByUser: &acct, Media: []uuid.UUID{logo},
		Deliveries: []model.IssueDelivery{{ID: uuid.New(), MailAccountID: &acct, Provider: "brevo", AccountName: "Brevo", Status: model.DeliverySent,
			CampaignID: "12", SentAt: &at, ResultsReadAt: &at, Results: model.MailResults{Recipients: 10, Delivered: 9, Clicks: 2},
			Audiences: []model.Audience{{ID: "list:7", Name: "Newsletter", Kind: "list", Size: 1200}}}}}
	tests := []struct {
		name, schema string
		view         any
	}{
		{"a draft", "Issue", core.ViewIssue(draft)},
		{"a sent issue", "Issue", core.ViewIssue(sent)},
		{"a mail account", "MailAccount", core.ViewMailAccount(&model.MailAccount{ID: acct, BrandID: uuid.New(), Provider: "brevo",
			Status: model.AdAccountActive})},
		{"a brand with a theme", "Brand", core.ViewBrand(&model.Brand{ID: uuid.New(), EmailTheme: model.EmailTheme{LogoMediaID: &logo}})},
		{"a brand without one", "Brand", core.ViewBrand(&model.Brand{ID: uuid.New()})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.view)
			if err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			if err := doc.Components.Schemas[tt.schema].Value.VisitJSON(v); err != nil {
				t.Fatalf("does not match the contract: %v\n%s", err, raw)
			}
		})
	}
}

// Reports render as the contract says, with empty sections as null.
func TestReportViewsMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	post := uuid.New()
	views := int64(40)
	full := &core.Report{Brand: &model.Brand{ID: uuid.New()}, Since: day, Until: day.AddDate(0, 1, -1), PrevSince: day.AddDate(0, -1, 0),
		PrevUntil:  day.AddDate(0, 0, -1),
		Publishing: &core.ReportPublishing{Published: core.Pair{Now: 3, Before: 2}},
		Engagement: &core.ReportEngagement{Likes: core.Pair{Now: 9}, TopPosts: []core.EngagementRow{{ID: &post, Label: "x",
			Counts: platform.Counts{Likes: 9, Views: &views}}}},
		Web:         &core.ReportWeb{Visitors: core.Pair{Now: 100}},
		Ads:         &core.ReportAds{Totals: []core.ReportSpend{{Currency: "USD", Spend: core.Pair{Now: 1000}}}},
		Newsletters: &core.ReportNewsletters{Issues: core.Pair{Now: 1}, Sent: []*model.Issue{{ID: uuid.New(), Subject: "x", SendAt: &day}}}}
	empty := &core.Report{Brand: &model.Brand{ID: uuid.New()}, Since: day, Until: day, PrevSince: day, PrevUntil: day}
	for name, r := range map[string]*core.Report{"full": full, "empty": empty} {
		raw, err := json.Marshal(core.ViewReport(r))
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if err := doc.Components.Schemas["Report"].Value.VisitJSON(v); err != nil {
			t.Fatalf("%s: does not match the contract: %v\n%s", name, err, raw)
		}
	}
}

// A credential's description of itself renders as the contract says.
func TestMeViewMatchesContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	key := core.ViewAPIKey(&model.APIKey{ID: uuid.New(), Name: "cli", Hint: "abcd", Scopes: []string{"posts:read"}, CreatedAt: time.Now()})
	me := core.MeView{Object: "me", Livemode: true, Org: core.MeOrgView{ID: "org_1", Name: "Otium"}, APIKey: &key}
	raw, err := json.Marshal(me)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := doc.Components.Schemas["Me"].Value.VisitJSON(v); err != nil {
		t.Fatalf("does not match the contract: %v\n%s", err, raw)
	}
}
