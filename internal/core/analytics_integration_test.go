// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Visits and signups are matched to the post whose link brought them, by
// the tags Araldo wrote (ADR 0025).
func TestAnalyticsAttributesSignupsToPosts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Shipped the batch API"},
		PublishAt: time.Now().Add(48 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	pid := id.Format(id.Post, p.ID)
	tags := fmt.Sprintf("bluesky/social/release/%s, reddit/paid/alpha-launch/localllama-1", pid)
	src, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Goals: []string{" Waitlist Signup ", "Waitlist Signup", "Docs"}, Fields: map[string]string{"site": "openb00ks.example", "tags": tags}})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Goals) != 2 || src.Site != "openb00ks.example" {
		t.Fatalf("source %+v: goals are trimmed and deduplicated", src)
	}
	if _, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Fields: map[string]string{"site": "openb00ks.example"}}); kind(err) != apperr.KindConflict {
		t.Fatalf("the same site twice: %v", err)
	}
	waitFor(t, "the source to be read", func() {
		if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		got, err := w.s.AnalyticsSource(ctx, w.owner, src.ID)
		return err == nil && got.ReadAt != nil
	})

	until := ads.Date(time.Now(), time.UTC)
	sum := func(group store.AnalyticsGroup) *core.AnalyticsSummary {
		t.Helper()
		s, err := w.s.AnalyticsSummary(ctx, w.owner, core.AnalyticsFilter{GroupBy: group, Since: until.AddDate(0, 0, -29), Until: until, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	byPost := sum(store.AnalyticsByPost)
	if len(byPost.Rows) != 1 || byPost.Rows[0].Key != pid || byPost.Rows[0].Label != "Shipped the batch API" {
		t.Fatalf("by post: %+v", byPost.Rows)
	}
	row := byPost.Rows[0]
	if row.Visitors == 0 || row.Conversions == 0 || row.Goals["Waitlist Signup"] == 0 || row.Goals["Docs"] == 0 ||
		row.Conversions != row.Goals["Waitlist Signup"]+row.Goals["Docs"] {
		t.Fatalf("the post's counts %+v", row)
	}
	if byPost.Totals.Untagged == 0 || byPost.Totals.Untagged >= byPost.Totals.Visitors {
		t.Fatalf("totals %+v: untagged traffic is counted, and is part of all traffic", byPost.Totals)
	}
	bySource := sum(store.AnalyticsBySource)
	if len(bySource.Rows) != 2 {
		t.Fatalf("by source: %+v (the ad's link counts too; untagged traffic does not)", bySource.Rows)
	}
	if byDay := sum(store.AnalyticsByDay); len(byDay.Rows) != core.AnalyticsBackfillDays {
		t.Fatalf("by day: %d rows, want %d", len(byDay.Rows), core.AnalyticsBackfillDays)
	}

	// Re-reading rewrites the days: nothing doubles.
	w.s.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	if again := sum(store.AnalyticsByPost); again.Rows[0].Visitors != row.Visitors {
		t.Fatalf("after a re-read the post has %d visitors, want %d", again.Rows[0].Visitors, row.Visitors)
	}

	if err := w.s.DeleteAnalyticsSource(ctx, w.owner, src.ID); err != nil {
		t.Fatal(err)
	}
	if gone := sum(store.AnalyticsByPost); len(gone.Rows) != 0 || gone.Totals.Visitors != 0 {
		t.Fatalf("after disconnecting: %+v", gone)
	}
}

func TestAnalyticsSourceRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	if _, err := w.s.ConnectAnalyticsSource(ctx, live, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox}); kind(err) != apperr.KindInvalid {
		t.Fatalf("the sandbox in live mode: %v", err)
	}
	if _, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: "plausible"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a real provider in test mode: %v", err)
	}
	many := make([]string, core.MaxAnalyticsGoals+1)
	for i := range many {
		many[i] = fmt.Sprintf("goal %d", i)
	}
	if _, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Goals: many}); kind(err) != apperr.KindInvalid {
		t.Fatalf("too many goals: %v", err)
	}
	revoked, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Fields: map[string]string{"site": "revoked.example", "simulate": "auth_revoked"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the revoked source flagged", func() {
		if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		got, err := w.s.AnalyticsSource(ctx, w.owner, revoked.ID)
		return err == nil && got.Status == model.AdAccountNeedsReauth
	})
	if _, err := w.s.AnalyticsSummary(ctx, w.owner, core.AnalyticsFilter{GroupBy: "hour"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("an unknown grouping: %v", err)
	}
}

// An ad campaign is credited with the signups its tagged links brought:
// utm_source is the network, utm_medium paid and utm_campaign the
// campaign's name as a token (ADR 0025).
func TestAdCampaignsGetSignups(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	ac := connectSandboxAds(t, w, w.owner, "Open B00KS", nil)
	src, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Goals: []string{"Waitlist Signup"}, Fields: map[string]string{"site": "openb00ks.example", "tags": "sandbox/paid/launch/ad-1, sandbox/social/retargeting/x"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ad account and the source to be read", func() {
		if _, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		a, err := w.s.AdAccount(ctx, w.owner, ac.ID)
		s, err2 := w.s.AnalyticsSource(ctx, w.owner, src.ID)
		return err == nil && err2 == nil && a.ReadAt != nil && s.ReadAt != nil
	})

	until := ads.Date(time.Now(), time.UTC)
	sum, err := w.s.AdsSummary(ctx, w.owner, core.AdsFilter{GroupBy: store.AdsByCampaign, Since: until.AddDate(0, 0, -29), Until: until, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Rows) != 2 {
		t.Fatalf("campaigns: %+v", sum.Rows)
	}
	for _, r := range sum.Rows {
		switch r.Label {
		case "Launch":
			if r.Visitors == 0 || r.Signups == 0 {
				t.Fatalf("Launch has tagged paid traffic, got %+v", r)
			}
		case "Retargeting":
			if r.Visitors != 0 || r.Signups != 0 {
				t.Fatalf("Retargeting's only traffic is not paid, got %+v", r)
			}
		}
	}
	view := core.ViewAdsSummary(sum)
	for _, r := range view.Data {
		if r.Label == "Launch" && r.CostPerSignup != core.CostPer(r.Spend, r.Signups) {
			t.Fatalf("cost per signup %+v", r)
		}
	}

	byAccount, err := w.s.AdsSummary(ctx, w.owner, core.AdsFilter{GroupBy: store.AdsByAccount, Since: until.AddDate(0, 0, -29), Until: until, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if byAccount.Rows[0].Signups != 0 {
		t.Fatalf("signups are credited to campaigns only: %+v", byAccount.Rows[0])
	}
}
