// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// TestDailySummary checks a person who takes a daily summary gets their
// org notifications in one email at 8:00 in their time zone, account
// notices at once, everything waiting at once when they turn it off, and
// that its unsubscribe turns off all org notification email (ADR 0034).
func TestDailySummary(t *testing.T) {
	t.Parallel()
	mail := &flakySender{}
	w := newWorld(t, withMail(mail))
	ctx := t.Context()
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC) // 13:00 in Denver
	w.s.Now = func() time.Time { return now }
	send := func() int {
		t.Helper()
		n, err := core.SendNotificationEmailsTo(w.s, w.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	notify := func(typ, subject string) {
		t.Helper()
		if err := core.NotifyOrg(w.s, w.org.ID, true, typ, subject, "", []uuid.UUID{w.user.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.s.SetNotificationEmailSetting(ctx, w.user.ID, core.EmailSetting{Digest: true, Timezone: "Mars/Olympus"}); code(err) != "timezone_invalid" {
		t.Fatalf("an unknown time zone: %v", err)
	}
	if err := w.s.SetNotificationEmailSetting(ctx, w.user.ID, core.EmailSetting{Digest: true, Timezone: "America/Denver"}); err != nil {
		t.Fatal(err)
	}
	if got, err := w.s.NotificationEmailSetting(ctx, w.user.ID); err != nil || !got.Digest || got.Timezone != "America/Denver" {
		t.Fatalf("the setting: %+v, %v", got, err)
	}
	notify(core.NotifyPostApproval, "A post waits")
	notify(core.NotifyChannelReauth, "Reconnect Bluesky")
	if err := w.s.ChangePassword(ctx, w.session, "correct horse battery", "a whole new password"); err != nil {
		t.Fatal(err)
	}
	if n := send(); n != 1 || len(mail.to(w.user.Email)) != 1 || !strings.Contains(mail.to(w.user.Email)[0].Subject, "password") {
		t.Fatalf("before 8:00: sent %d, want the account notice only", n)
	}

	// 8:00 in Denver the next day: one summary of both.
	now = time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	if n := send(); n != 1 {
		t.Fatalf("at 8:00: sent %d emails, want one summary", n)
	}
	sent := mail.to(w.user.Email)
	summary := sent[len(sent)-1]
	if len(sent) != 2 || summary.Subject != "Araldo: 2 things since yesterday" || !strings.Contains(summary.Text, "A post waits") ||
		!strings.Contains(summary.Text, "Reconnect Bluesky") || summary.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("the summary: %+v", summary)
	}
	for _, n := range inbox(t, w, w.user.ID, core.NotifyPostApproval) {
		if emailOf(t, n) != "sent" {
			t.Fatalf("a summarized notification: %s", emailOf(t, n))
		}
	}

	// Turning it off sends what waits at once, one by one.
	notify(core.NotifyPostApproval, "Another post waits")
	if n := send(); n != 0 {
		t.Fatalf("sent %d before the next summary", n)
	}
	if err := w.s.SetNotificationEmailSetting(ctx, w.user.ID, core.EmailSetting{Timezone: "America/Denver"}); err != nil {
		t.Fatal(err)
	}
	if n := send(); n != 1 || !strings.Contains(mail.to(w.user.Email)[2].Subject, "Another post waits") {
		t.Fatalf("after turning it off: sent %d", n)
	}

	// The summary's unsubscribe turns off all org notification email.
	token := strings.TrimSuffix(strings.TrimPrefix(summary.Headers["List-Unsubscribe"], "<https://araldo.test/unsubscribe/"), ">")
	if target, err := w.s.Unsubscribe(ctx, token); err != nil || !target.All {
		t.Fatalf("unsubscribing: %+v, %v", target, err)
	}
	choices, err := w.s.NotificationChoices(ctx, w.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range choices {
		if c.Email || !c.InApp {
			t.Errorf("after unsubscribing from the summary, %s: in app %v, email %v", c.Key, c.InApp, c.Email)
		}
	}
}
