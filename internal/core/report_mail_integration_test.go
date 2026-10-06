// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestMonthlyReportEmail checks the monthly report by email: admins choose
// the addresses; early in a month, in the brand's time zone, the month
// before goes out once, as a share link that works; failures are retried;
// an address removed meanwhile is not sent to; and its unsubscribe link
// takes the address off (ADR 0026).
func TestMonthlyReportEmail(t *testing.T) {
	t.Parallel()
	mail := &flakySender{}
	w := newWorld(t, withMail(mail)) // the brand is in America/Denver
	ctx := t.Context()
	run := func() int {
		t.Helper()
		n, err := core.MailReportsOrg(w.s, w.org.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	db := open(t)
	status := func(month, email string) string {
		t.Helper()
		st, reason, err := db.ReportEmailStatus(ctx, w.org.ID, w.brand.ID, month, email)
		if err != nil {
			return "none"
		}
		if reason != "" {
			return st + ": " + reason
		}
		return st
	}

	// Choosing who gets it.
	for _, email := range []string{"Client@Example.com", "ops@example.com"} {
		if err := w.s.AddReportRecipient(ctx, w.owner, w.brand.ID, email); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.s.AddReportRecipient(ctx, w.owner, w.brand.ID, "client@example.com"); code(err) != "recipient_exists" {
		t.Fatalf("an address twice: %v", err)
	}
	if err := w.s.AddReportRecipient(ctx, w.owner, w.brand.ID, "not an address"); code(err) != "email_invalid" {
		t.Fatalf("a malformed address: %v", err)
	}
	viewer := w.owner
	viewer.Role = model.RoleViewer
	if _, err := w.s.ReportRecipients(ctx, viewer, w.brand.ID); err == nil {
		t.Fatal("a viewer listed the recipients")
	}
	other := newWorld(t)
	if err := other.s.AddReportRecipient(ctx, other.owner, w.brand.ID, "x@example.com"); kind(err) != apperr.KindNotFound {
		t.Fatalf("another org's brand: %v", err)
	}

	// Not in the middle of a month.
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	w.s.Now = func() time.Time { return now }
	if n := run(); n != 0 || status("2026-10", "client@example.com") != "none" {
		t.Fatalf("mid-month: sent %d", n)
	}

	// Early December in Denver (still November 30 there at 03:00 UTC): not
	// yet; at 08:00 UTC it is December 1 there, and November goes out. The
	// first try fails and is retried.
	now = time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC)
	if n := run(); n != 0 || status("2026-11", "client@example.com") != "none" {
		t.Fatalf("before the month turned in the brand's zone: sent %d", n)
	}
	now = time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	mail.failWith(errors.New("connection refused"))
	if n := run(); n != 0 || !strings.HasPrefix(status("2026-11", "client@example.com"), "queued: connection refused") {
		t.Fatalf("a failed first try: sent %d, %s", n, status("2026-11", "client@example.com"))
	}
	mail.failWith(nil)
	if err := w.s.RemoveReportRecipient(ctx, w.owner, w.brand.ID, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if n := run(); n != 1 || status("2026-11", "ops@example.com") != "suppressed: no longer a recipient" {
		t.Fatalf("the retry: sent %d, ops %s", n, status("2026-11", "ops@example.com"))
	}
	if n := run(); n != 0 {
		t.Fatalf("sent %d more the same month", n)
	}

	got := mail.to("client@example.com")
	if len(got) != 1 || !strings.Contains(got[0].Subject, "November 2026") || got[0].Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("the email: %+v", got)
	}
	m := strings.SplitN(strings.SplitN(got[0].Text, "https://araldo.test/r/", 2)[1], "\n", 2)
	token := strings.TrimSpace(m[0])
	if r, sh, err := w.s.SharedReport(ctx, token); err != nil || sh.Month != "2026-11" || !sh.Livemode || r.Brand.ID != w.brand.ID {
		t.Fatalf("the email's link: %v", err)
	}

	// Its unsubscribe link takes the address off; the next month sends nothing.
	unsub := strings.TrimSuffix(strings.TrimPrefix(got[0].Headers["List-Unsubscribe"], "<https://araldo.test/unsubscribe/"), ">")
	target, err := w.s.Unsubscribe(ctx, unsub)
	if err != nil || target.Brand == nil || target.Brand.ID != w.brand.ID || target.Email != "client@example.com" {
		t.Fatalf("unsubscribing: %+v, %v", target, err)
	}
	if left, err := w.s.ReportRecipients(ctx, w.owner, w.brand.ID); err != nil || len(left) != 0 {
		t.Fatalf("recipients after unsubscribing: %v, %v", left, err)
	}
	now = time.Date(2027, 1, 2, 12, 0, 0, 0, time.UTC)
	if n := run(); n != 0 || status("2026-12", "client@example.com") != "none" {
		t.Fatalf("after unsubscribing: sent %d", n)
	}
}
