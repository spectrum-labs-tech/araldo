// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// A report gathers every section from what the brand's sources gave, and
// shows a key only the sections its scopes allow (ADR 0026).
func TestBrandReport(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()

	// Publishing and engagement.
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Report-worthy post"}})
	if err != nil {
		t.Fatal(err)
	}
	published := *settle(t, w, p.ID).Targets[0].PublishedAt
	w.s.Now = func() time.Time { return published.Add(2 * time.Hour) }
	if _, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	w.s.Now = time.Now

	// Web analytics and ads.
	src, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Goals: []string{"Signup"}, Fields: map[string]string{"site": "report.example", "tags": "sandbox/paid/launch/ad-1"}})
	if err != nil {
		t.Fatal(err)
	}
	acct := connectSandboxAds(t, w, w.owner, "Report ads", nil)
	waitFor(t, "the analytics and ads to be read", func() {
		if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		s, err := w.s.AnalyticsSource(ctx, w.owner, src.ID)
		a, err2 := w.s.AdAccount(ctx, w.owner, acct.ID)
		return err == nil && err2 == nil && s.ReadAt != nil && a.ReadAt != nil
	})

	// A newsletter, sent.
	setTheme(t, w)
	connectSandboxMail(t, w, "news@araldo.dev", nil)
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "The monthly", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	sendAt := time.Now().Add(5 * time.Minute)
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, sendAt); err != nil {
		t.Fatal(err)
	}
	waitDelivery(t, w, is.ID, model.DeliveryHandedOff, func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	w.s.Now = func() time.Time { return sendAt.Add(20 * time.Minute) }
	waitDelivery(t, w, is.ID, model.DeliverySent, func() {
		if _, err := core.ReadNewsletterResultsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	w.s.Now = time.Now

	// The last week, through tomorrow so nothing near midnight falls out.
	today := ads.Date(time.Now(), time.UTC)
	in := core.ReportInput{BrandID: w.brand.ID, Since: today.AddDate(0, 0, -6), Until: today.AddDate(0, 0, 1)}
	r, err := w.s.BrandReport(ctx, w.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if !r.PrevUntil.Equal(in.Since.AddDate(0, 0, -1)) || !r.PrevSince.Equal(in.Since.AddDate(0, 0, -8)) || !r.Settling {
		t.Fatalf("periods %v..%v before %v..%v, settling %t", r.PrevSince, r.PrevUntil, r.Since, r.Until, r.Settling)
	}
	if r.Publishing == nil || r.Publishing.Published.Now != 1 || len(r.Publishing.ByNetwork) != 1 || r.Publishing.ByNetwork[0].Provider != "sandbox" {
		t.Fatalf("publishing %+v", r.Publishing)
	}
	if r.Engagement == nil || r.Engagement.Interactions().Now == 0 || len(r.Engagement.TopPosts) != 1 || *r.Engagement.TopPosts[0].ID != p.ID {
		t.Fatalf("engagement %+v", r.Engagement)
	}
	if r.Web == nil || r.Web.Visitors.Now == 0 || r.Web.Signups.Now == 0 || r.Web.Visitors.Before == 0 || len(r.Web.Sources) != 1 {
		t.Fatalf("web %+v", r.Web)
	}
	if r.Ads == nil || len(r.Ads.Totals) != 1 || r.Ads.Totals[0].Currency != "USD" || r.Ads.Totals[0].Spend.Now == 0 ||
		r.Ads.Totals[0].Spend.Before == 0 || r.Ads.Totals[0].Signups.Now == 0 || len(r.Ads.Campaigns) != 2 {
		t.Fatalf("ads %+v", r.Ads)
	}
	if r.Newsletters == nil || r.Newsletters.Issues.Now != 1 || r.Newsletters.Delivered.Now == 0 || r.Newsletters.Sent[0].ID != is.ID {
		t.Fatalf("newsletters %+v", r.Newsletters)
	}
	if v := core.ViewReport(r); v.Object != "report" || v.Ads == nil || len(v.Newsletters.Sent) != 1 {
		t.Fatalf("view %+v", v)
	}

	// A key that may read posts but not ads or newsletters sees neither.
	_, key, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "reports", Scopes: []string{"posts:read"}})
	if err != nil {
		t.Fatal(err)
	}
	ka := w.owner
	ka.UserID, ka.KeyID, ka.Scopes = nil, &key.ID, []string{"posts:read"}
	if r, err = w.s.BrandReport(ctx, ka, in); err != nil || r.Ads != nil || r.Newsletters != nil || r.Web == nil {
		t.Fatalf("a posts:read key: ads %v, newsletters %v, web %v, %v", r.Ads, r.Newsletters, r.Web, err)
	}

	// A brand with nothing has no sections.
	empty, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Quiet", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = w.s.BrandReport(ctx, w.owner, core.ReportInput{BrandID: empty.ID})
	if err != nil || r.Publishing != nil || r.Engagement != nil || r.Web != nil || r.Ads != nil || r.Newsletters != nil {
		t.Fatalf("an empty brand: %+v, %v", r, err)
	}
	if _, err := w.s.BrandReport(ctx, w.owner, core.ReportInput{BrandID: w.brand.ID, Month: "October"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a bad month: %v", err)
	}
}

// TestReportShares checks a share link shows its brand's month to anyone
// holding it, until it is withdrawn, expires or the org is suspended, and
// that only admins and owners of the org make and withdraw them.
func TestReportShares(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	month := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")

	token, sh, err := w.s.CreateReportShare(ctx, w.owner, w.brand.ID, month)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.s.ShareURL(token), "https://araldo.test/r/") {
		t.Fatalf("share URL %s", w.s.ShareURL(token))
	}
	r, got, err := w.s.SharedReport(ctx, token)
	if err != nil || got.ID != sh.ID || r.Brand.ID != w.brand.ID || r.Since.Format("2006-01") != month {
		t.Fatalf("the shared report: %+v, %+v, %v", r, got, err)
	}
	if list, err := w.s.ReportShares(ctx, w.owner, w.brand.ID); err != nil || len(list) != 1 || list[0].ID != sh.ID {
		t.Fatalf("open shares: %v, %v", list, err)
	}

	editorUser, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, err := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.CreateReportShare(ctx, editor, w.brand.ID, month); kind(err) != apperr.KindForbidden {
		t.Fatalf("an editor sharing: %v", err)
	}
	if _, _, err := w.s.CreateReportShare(ctx, w.owner, w.brand.ID, "last month"); kind(err) != apperr.KindInvalid {
		t.Fatalf("a month that is not one: %v", err)
	}
	other := newWorld(t)
	if err := other.s.RevokeReportShare(ctx, other.owner, sh.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("another org withdrawing it: %v", err)
	}
	for _, bad := range []string{"", "nope", token + "x"} {
		if _, _, err := w.s.SharedReport(ctx, bad); kind(err) != apperr.KindNotFound {
			t.Errorf("SharedReport(%q): %v", bad, err)
		}
	}

	// Withdrawn, it stops working at once.
	if err := w.s.RevokeReportShare(ctx, w.owner, sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.SharedReport(ctx, token); kind(err) != apperr.KindNotFound {
		t.Fatalf("a withdrawn link: %v", err)
	}

	// Another expires, and is refused while its org is suspended.
	token2, _, err := w.s.CreateReportShare(ctx, w.owner, w.brand.ID, month)
	if err != nil {
		t.Fatal(err)
	}
	_, op := operatorKey(t, w.s)
	in, _, err := w.s.InOrg(ctx, op, w.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	suspended, active := model.OrgSuspended, model.OrgActive
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Status: &suspended}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.SharedReport(ctx, token2); kind(err) != apperr.KindNotFound {
		t.Fatalf("a suspended org's link: %v", err)
	}
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Status: &active}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.SharedReport(ctx, token2); err != nil {
		t.Fatalf("once active again: %v", err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(core.ReportShareTTL + time.Minute) }
	if _, _, err := w.s.SharedReport(ctx, token2); kind(err) != apperr.KindNotFound {
		t.Fatalf("an expired link: %v", err)
	}
}
