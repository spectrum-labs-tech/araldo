// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

func connectSandboxMail(t *testing.T, w *world, from string, fields map[string]string) *model.MailAccount {
	t.Helper()
	ma, err := w.s.ConnectMailAccount(t.Context(), w.owner, core.MailAccountInput{BrandID: w.brand.ID, Provider: email.Sandbox,
		FromName: "AR15.build", FromEmail: from, DefaultAudiences: []string{"list-1"}, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	return ma
}

func setTheme(t *testing.T, w *world) {
	t.Helper()
	if _, err := w.s.SetEmailTheme(t.Context(), w.owner, w.brand.ID, core.EmailThemeInput{Accent: "#1d4ed8",
		PostalAddress: "1 Main St\nDenver, CO", Footer: "You signed up at ar15.build."}); err != nil {
		t.Fatal(err)
	}
}

// waitDelivery drives step until the issue's only delivery reaches want.
func waitDelivery(t *testing.T, w *world, issueID uuid.UUID, want model.DeliveryStatus, step func()) *model.Issue {
	t.Helper()
	var is *model.Issue
	waitFor(t, "the delivery to be "+string(want), step, func() bool {
		var err error
		is, err = w.s.Issue(t.Context(), w.owner, issueID)
		return err == nil && len(is.Deliveries) == 1 && is.Deliveries[0].Status == want
	})
	return is
}

func TestNewsletterIsHandedOffAndRead(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		UTMDomains: []string{"ar15.build"}}); err != nil {
		t.Fatal(err)
	}
	ma := connectSandboxMail(t, w, "News@AR15.build", nil)
	if ma.FromEmail != "news@ar15.build" || ma.DefaultAudiences[0] != "list-1" {
		t.Fatalf("account %+v", ma)
	}
	logo, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 200, 80), Filename: "logo.png", Alt: "Logo"})
	if err != nil {
		t.Fatal(err)
	}
	hero, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 1200, 600), Filename: "hero.png", Alt: "A rifle"})
	if err != nil {
		t.Fatal(err)
	}

	body := "# Builds of the week\n\n![](" + id.Format(id.Media, hero.ID) + ")\n\nSee [the Recce](https://ar15.build/b/1) and https://elsewhere.example.\n\n" +
		"[Start a build](https://ar15.build/new){.button}"
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "Builds of the week", PreviewText: "Three rifles", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueDraft || len(is.Deliveries) != 1 || is.Deliveries[0].Audiences[0].Name != "Newsletter" ||
		is.Deliveries[0].Status != model.DeliveryDraft || len(is.Media) != 1 || is.Media[0] != hero.ID {
		t.Fatalf("draft %+v", is)
	}

	r, err := w.s.IssuePreview(ctx, w.owner, is.ID)
	if err != nil {
		t.Fatal(err)
	}
	nl := id.Format(id.Issue, is.ID)
	for _, want := range []string{
		"https://ar15.build/b/1?utm_campaign=" + nl + "&amp;utm_content=link-1&amp;utm_medium=email&amp;utm_source=sandbox",
		"https://ar15.build/new?utm_campaign=" + nl + "&amp;utm_content=link-3&amp;utm_medium=email&amp;utm_source=sandbox",
		`href="https://elsewhere.example"`,
		"/v1/media/" + id.Format(id.Media, hero.ID) + "/content?signature=",
	} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("preview lacks %s", want)
		}
	}

	// Scheduling needs the postal address the law asks for.
	sendAt := time.Now().Add(5 * time.Minute)
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, sendAt); kind(err) != apperr.KindInvalid {
		t.Fatalf("scheduling without a postal address: %v", err)
	}
	if _, err := w.s.SetEmailTheme(ctx, w.owner, w.brand.ID, core.EmailThemeInput{Accent: "#fde047"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("an accent too light to read: %v", err)
	}
	if _, err := w.s.SetEmailTheme(ctx, w.owner, w.brand.ID, core.EmailThemeInput{LogoMediaID: &logo.ID, Accent: "#1D4ED8",
		PostalAddress: "1 Main St\nDenver, CO"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now()); kind(err) != apperr.KindInvalid {
		t.Fatalf("scheduling for now: %v", err)
	}
	is, err = w.s.ScheduleIssue(ctx, w.owner, is.ID, sendAt)
	if err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueScheduled || is.Deliveries[0].Status != model.DeliveryQueued || is.Deliveries[0].HandoffAt == nil {
		t.Fatalf("scheduled %+v", is.Deliveries[0])
	}
	if _, err := w.s.UpdateIssue(ctx, w.owner, is.ID, core.IssueInput{Subject: "x", Body: "y"}); kind(err) != apperr.KindConflict {
		t.Fatalf("editing a scheduled issue: %v", err)
	}
	if err := w.s.DeleteMailAccount(ctx, w.owner, ma.ID); kind(err) != apperr.KindConflict {
		t.Fatalf("deleting an account an issue goes through: %v", err)
	}
	if err := w.s.DeleteMedia(ctx, w.owner, hero.ID); err == nil {
		t.Fatal("an issue's image was deleted")
	}

	// Due for hand-off at once: it goes out within a day.
	is = waitDelivery(t, w, is.ID, model.DeliveryHandedOff, func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	if d := is.Deliveries[0]; !strings.HasPrefix(d.CampaignID, "sbx_") || d.Attempts != 1 || is.Status != model.IssueScheduled {
		t.Fatalf("handed off %+v (issue %s)", d, is.Status)
	}

	// After the send time, the provider reports it sent, with results.
	w.s.Now = func() time.Time { return sendAt.Add(20 * time.Minute) }
	is = waitDelivery(t, w, is.ID, model.DeliverySent, func() {
		if _, err := core.ReadNewsletterResultsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	d := is.Deliveries[0]
	if is.Status != model.IssueSent || d.SentAt == nil || d.Results.Delivered == 0 || d.NextReadAt == nil ||
		!d.NextReadAt.Equal(d.SentAt.Add(time.Hour)) {
		t.Fatalf("sent %+v (issue %s)", d, is.Status)
	}
	if v := core.ViewIssue(is); v.Results.Delivered != d.Results.Delivered || v.Results.Clicks == 0 {
		t.Fatalf("issue results %+v", v.Results)
	}
	if _, err := w.s.CancelIssue(ctx, w.owner, is.ID); kind(err) != apperr.KindConflict {
		t.Fatalf("canceling a sent issue: %v", err)
	}

	// The brand's analytics credit the issue's links to it by name.
	w.s.Now = time.Now
	src, err := w.s.ConnectAnalyticsSource(ctx, w.owner, core.AnalyticsSourceInput{BrandID: w.brand.ID, Provider: analytics.Sandbox,
		Goals: []string{"Signup"}, Fields: map[string]string{"site": "ar15.build", "tags": "sandbox/email/" + nl + "/link-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the analytics source to be read", func() {
		if _, err := core.CollectAnalyticsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		got, err := w.s.AnalyticsSource(ctx, w.owner, src.ID)
		return err == nil && got.ReadAt != nil
	})
	until := time.Now().UTC().Truncate(24 * time.Hour)
	byCampaign, err := w.s.AnalyticsSummary(ctx, w.owner, core.AnalyticsFilter{GroupBy: store.AnalyticsByCampaign, Since: until.AddDate(0, 0, -6),
		Until: until, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(byCampaign.Rows) != 1 || byCampaign.Rows[0].Key != nl || byCampaign.Rows[0].Label != "Newsletter: Builds of the week" ||
		byCampaign.Rows[0].Conversions == 0 {
		t.Fatalf("by campaign: %+v", byCampaign.Rows)
	}
}

func TestNewsletterApprovalMovesAndStops(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	setTheme(t, w)
	connectSandboxMail(t, w, "news@ar15.build", nil)
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalEditorsAndKey}); err != nil {
		t.Fatal(err)
	}
	editorUser, err := w.s.AddMember(ctx, w.owner, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, err := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	is, err := w.s.CreateIssue(ctx, editor, core.IssueInput{BrandID: w.brand.ID, Subject: "October", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	sendAt := time.Now().Add(48 * time.Hour)
	if is, err = w.s.ScheduleIssue(ctx, editor, is.ID, sendAt); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssuePendingApproval || is.Deliveries[0].Status != model.DeliveryHeld {
		t.Fatalf("an editor's issue: %s / %s", is.Status, is.Deliveries[0].Status)
	}
	if _, err := w.s.ReviewIssue(ctx, editor, is.ID, true, ""); kind(err) != apperr.KindForbidden {
		t.Fatalf("an editor approving: %v", err)
	}
	if is, err = w.s.ReviewIssue(ctx, w.owner, is.ID, false, "Add the October builds"); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueDraft || is.ReviewNote != "Add the October builds" || is.Deliveries[0].Status != model.DeliveryDraft {
		t.Fatalf("rejected: %s, %q, %s", is.Status, is.ReviewNote, is.Deliveries[0].Status)
	}
	if is, err = w.s.UpdateIssue(ctx, editor, is.ID, core.IssueInput{Subject: "October builds", Body: "Hello, with builds"}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.ScheduleIssue(ctx, editor, is.ID, sendAt); err != nil {
		t.Fatal(err)
	}
	if is, err = w.s.ReviewIssue(ctx, w.owner, is.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueScheduled || is.Deliveries[0].Status != model.DeliveryQueued ||
		!is.Deliveries[0].HandoffAt.Equal(is.SendAt.Add(-core.HandoffAhead)) {
		t.Fatalf("approved: %s, %+v", is.Status, is.Deliveries[0])
	}

	// Moving keeps it approved, and moves the hand-off.
	later := sendAt.Add(24 * time.Hour).UTC().Truncate(time.Second)
	if is, err = w.s.ScheduleIssue(ctx, editor, is.ID, later); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueScheduled || !is.SendAt.Equal(later) || !is.Deliveries[0].HandoffAt.Equal(later.Add(-core.HandoffAhead)) {
		t.Fatalf("moved: %s at %v, hand-off %v", is.Status, is.SendAt, is.Deliveries[0].HandoffAt)
	}

	// Unscheduling returns a draft that needs approval again.
	if is, err = w.s.UnscheduleIssue(ctx, editor, is.ID); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueDraft || is.Deliveries[0].Status != model.DeliveryDraft || is.Deliveries[0].HandoffAt != nil {
		t.Fatalf("unscheduled: %s, %+v", is.Status, is.Deliveries[0])
	}

	// The owner needs no approval; once handed off, canceling removes the
	// campaign too.
	if is, err = w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now().Add(10*time.Minute)); err != nil || is.Status != model.IssueScheduled {
		t.Fatalf("owner scheduling: %v", err)
	}
	waitDelivery(t, w, is.ID, model.DeliveryHandedOff, func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	if is, err = w.s.CancelIssue(ctx, w.owner, is.ID); err != nil {
		t.Fatal(err)
	}
	if is.Status != model.IssueCanceled || is.Deliveries[0].Status != model.DeliveryCanceled {
		t.Fatalf("canceled: %s, %s", is.Status, is.Deliveries[0].Status)
	}
}

// A hand-off whose answer is lost is found by its tag, never sent twice.
func TestNewsletterHandoffRecoversAnUnknownCreate(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	setTheme(t, w)
	connectSandboxMail(t, w, "news@ar15.build", map[string]string{"simulate": "uncertain"})
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "Hi", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a first, unknown, attempt", func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		got, err := w.s.Issue(ctx, w.owner, is.ID)
		return err == nil && got.Deliveries[0].Attempts >= 1 && got.Deliveries[0].LastError != ""
	})
	// The retry is due after a back-off.
	w.s.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	got := waitDelivery(t, w, is.ID, model.DeliveryHandedOff, func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	if d := got.Deliveries[0]; !strings.HasPrefix(d.CampaignID, "sbx_") || d.Attempts < 2 || d.LastError != "" {
		t.Fatalf("recovered %+v", d)
	}
}

func TestNewsletterStoppedAtTheProvider(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	setTheme(t, w)
	connectSandboxMail(t, w, "news@ar15.build", map[string]string{"simulate": "stopped"})
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "Hi", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	sendAt := time.Now().Add(5 * time.Minute)
	if _, err = w.s.ScheduleIssue(ctx, w.owner, is.ID, sendAt); err != nil {
		t.Fatal(err)
	}
	waitDelivery(t, w, is.ID, model.DeliveryHandedOff, func() {
		if _, err := core.HandOffNewslettersOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	w.s.Now = func() time.Time { return sendAt.Add(20 * time.Minute) }
	got := waitDelivery(t, w, is.ID, model.DeliveryCanceled, func() {
		if _, err := core.ReadNewsletterResultsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	})
	if got.Status != model.IssueCanceled || !strings.Contains(got.Deliveries[0].LastError, "Stopped") {
		t.Fatalf("stopped: %s, %q", got.Status, got.Deliveries[0].LastError)
	}
}

func TestNewsletterRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	if _, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, Provider: email.Sandbox,
		FromName: "A", FromEmail: "a@ar15.build"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a sandbox account in live mode: %v", err)
	}
	if _, err := w.s.ConnectMailAccount(ctx, w.owner, core.MailAccountInput{BrandID: w.brand.ID, Provider: "brevo",
		FromName: "A", FromEmail: "a@ar15.build"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a real provider in test mode: %v", err)
	}
	for _, in := range []core.MailAccountInput{
		{FromName: "", FromEmail: "a@ar15.build"},
		{FromName: "A", FromEmail: "A <a@ar15.build>"},
		{FromName: "A", FromEmail: "a@ar15.build", ReplyTo: "nope"},
		{FromName: "A", FromEmail: "a@ar15.build", DefaultAudiences: []string{"list-404"}},
	} {
		in.BrandID, in.Provider = w.brand.ID, email.Sandbox
		if _, err := w.s.ConnectMailAccount(ctx, w.owner, in); kind(err) != apperr.KindInvalid {
			t.Errorf("connecting %+v: %v", in, err)
		}
	}
	ma := connectSandboxMail(t, w, "news@ar15.build", nil)
	if _, err := w.s.ConnectMailAccount(ctx, w.owner, core.MailAccountInput{BrandID: w.brand.ID, Provider: email.Sandbox,
		FromName: "Again", FromEmail: "news@ar15.build"}); kind(err) != apperr.KindConflict {
		t.Fatalf("the same sender twice: %v", err)
	}
	if aud, err := w.s.MailAudiences(ctx, w.owner, ma.ID); err != nil || len(aud) != len(email.SandboxAudiences) {
		t.Fatalf("audiences: %v, %v", aud, err)
	}
	ma, err := w.s.UpdateMailAccount(ctx, w.owner, ma.ID, core.MailAccountUpdate{FromName: "AR15.build news", FromEmail: "news@ar15.build",
		ReplyTo: "hello@ar15.build", DefaultAudiences: []string{"list-1", "segment-1"}})
	if err != nil || len(ma.DefaultAudiences) != 2 || ma.ReplyTo != "hello@ar15.build" {
		t.Fatalf("updated %+v, %v", ma, err)
	}

	for _, in := range []core.IssueInput{
		{Subject: "", Body: "x"},
		{Subject: "x", Body: ""},
		{Subject: "x", Body: "![](https://cdn.example/a.png)\n\n![](media_01h2xcejqtf2nbrexx3vqjhp41)"},
		{Subject: "x", Body: "x", Deliveries: []core.DeliveryInput{{MailAccountID: uuid.New()}}},
		{Subject: "x", Body: "x", Deliveries: []core.DeliveryInput{{MailAccountID: ma.ID, Audiences: []string{"list-404"}}}},
		{Subject: "x", Body: "x", Deliveries: []core.DeliveryInput{{MailAccountID: ma.ID}, {MailAccountID: ma.ID}}},
	} {
		in.BrandID = w.brand.ID
		if _, err := w.s.CreateIssue(ctx, w.owner, in); kind(err) != apperr.KindInvalid {
			t.Errorf("creating %+v: %v", in, err)
		}
	}
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "Hi", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(is.Deliveries[0].Audiences) != 2 {
		t.Fatalf("the account's defaults: %+v", is.Deliveries[0].Audiences)
	}
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now().Add(400*24*time.Hour)); kind(err) != apperr.KindInvalid {
		t.Fatalf("more than a year ahead: %v", err)
	}

	// Test sends: a handful of valid addresses, through the brand's account.
	test := func(to ...string) error {
		return w.s.SendTestIssue(ctx, w.owner, is.ID, core.TestIssueInput{MailAccountID: ma.ID, To: to})
	}
	if err := test("me@ar15.build", " me@ar15.build "); err != nil {
		t.Fatal(err)
	}
	if err := test(); kind(err) != apperr.KindInvalid {
		t.Fatalf("no addresses: %v", err)
	}
	if err := test("a@x.example", "b@x.example", "c@x.example", "d@x.example", "e@x.example", "f@x.example"); kind(err) != apperr.KindInvalid {
		t.Fatalf("too many addresses: %v", err)
	}
	if err := test("not an address"); kind(err) != apperr.KindInvalid {
		t.Fatalf("a bad address: %v", err)
	}

	// Viewers read; only editors write.
	viewerUser, err := w.s.AddMember(ctx, w.owner, fmt.Sprintf("viewer-%s@example.com", uuid.NewString()[:8]), model.RoleViewer, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	viewer, _, err := w.s.MemberActor(ctx, viewerUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Issue(ctx, viewer, is.ID); err != nil {
		t.Fatalf("a viewer reading: %v", err)
	}
	if _, err := w.s.CreateIssue(ctx, viewer, core.IssueInput{BrandID: w.brand.ID, Subject: "x", Body: "x"}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a viewer writing: %v", err)
	}

	// Disconnecting an account with only drafts keeps their history.
	if err := w.s.DeleteMailAccount(ctx, w.owner, ma.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Issue(ctx, w.owner, is.ID)
	if err != nil || got.Deliveries[0].MailAccountID != nil || got.Deliveries[0].AccountName == "" {
		t.Fatalf("after disconnecting: %+v, %v", got.Deliveries, err)
	}
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now().Add(time.Hour)); kind(err) != apperr.KindInvalid {
		t.Fatalf("scheduling through a disconnected account: %v", err)
	}
}
