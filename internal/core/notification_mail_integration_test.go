// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// flakySender fails while fail is set, then sends to its outbox.
type flakySender struct {
	outbox
	mu   sync.Mutex
	fail error
}

func (f *flakySender) Send(ctx context.Context, m smtpmail.Message) error {
	f.mu.Lock()
	err := f.fail
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.outbox.Send(ctx, m)
}

func (f *flakySender) failWith(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

// TestNotificationEmails checks the sender: account notices go without an
// unsubscribe, org ones with a one-click unsubscribe that turns that type
// off, failures are retried later or given up on, and a person gets at
// most 30 an hour (ADR 0034).
func TestNotificationEmails(t *testing.T) {
	t.Parallel()
	mail := &flakySender{}
	w := newWorld(t, withMail(mail))
	ctx := t.Context()
	send := func() int {
		t.Helper()
		n, err := core.SendNotificationEmailsTo(w.s, w.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// An account notice.
	if err := w.s.ChangePassword(ctx, w.session, "correct horse battery", "a whole new password"); err != nil {
		t.Fatal(err)
	}
	if n := send(); n != 1 {
		t.Fatalf("sent %d", n)
	}
	got := mail.to(w.user.Email)
	notice := inbox(t, w, w.user.ID, core.NotifyPasswordChanged)[0]
	if len(got) != 1 || got[0].Subject != "Your Araldo password was changed" || got[0].Headers["List-Unsubscribe"] != "" ||
		!strings.Contains(got[0].Text, "https://araldo.test/notifications/"+notice.ID.String()) || emailOf(t, notice) != "sent" {
		t.Fatalf("the account notice: %+v", got)
	}
	if n := send(); n != 0 {
		t.Fatalf("sent %d again", n)
	}

	// An org notification, and its one-click unsubscribe.
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyPostApproval, "A post waits", "", []uuid.UUID{w.user.ID}, nil); err != nil {
		t.Fatal(err)
	}
	send()
	got = mail.to(w.user.Email)
	unsub := got[1].Headers["List-Unsubscribe"]
	if len(got) != 2 || got[1].Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.HasPrefix(unsub, "<https://araldo.test/unsubscribe/") {
		t.Fatalf("the org notification's headers: %v", got[1].Headers)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(unsub, "<https://araldo.test/unsubscribe/"), ">")
	if !strings.Contains(got[1].Text, "/unsubscribe/"+token) {
		t.Fatal("the footer has no unsubscribe link")
	}
	if _, err := w.s.Unsubscribe(ctx, token[:len(token)-2]+"xx"); code(err) != "unsubscribe_link_invalid" {
		t.Fatalf("a tampered link: %v", err)
	}
	target, err := w.s.Unsubscribe(ctx, token)
	if err != nil || target.UserID != w.user.ID || target.Type.Key != core.NotifyPostApproval || target.OrgName != w.org.Name {
		t.Fatalf("unsubscribing: %+v, %v", target, err)
	}
	choices, _ := w.s.NotificationChoices(ctx, w.owner)
	for _, c := range choices {
		if c.Key == core.NotifyPostApproval && (c.Email || !c.InApp) {
			t.Fatalf("after unsubscribing: %+v", c)
		}
	}

	// A failure is retried later; a permanent one is given up on.
	now := time.Now()
	w.s.Now = func() time.Time { return now }
	mail.failWith(errors.New("connection refused"))
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyChannelReauth, "Reconnect", "", []uuid.UUID{w.user.ID}, nil); err != nil {
		t.Fatal(err)
	}
	reauth := inbox(t, w, w.user.ID, core.NotifyChannelReauth)[0]
	send()
	if s := emailOf(t, reauth); !strings.HasPrefix(s, "queued: connection refused") {
		t.Fatalf("after a failure: %s", s)
	}
	mail.failWith(nil)
	if n := send(); n != 0 {
		t.Fatalf("retried at once: %d", n)
	}
	now = now.Add(2 * time.Minute)
	if n := send(); n != 1 || emailOf(t, reauth) != "sent" {
		t.Fatalf("the retry: %d, %s", n, emailOf(t, reauth))
	}
	mail.failWith(&smtpmail.PermanentError{Err: &textproto.Error{Code: 550, Msg: "no such user"}})
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyChannelReauth, "Reconnect again", "", []uuid.UUID{w.user.ID}, nil); err != nil {
		t.Fatal(err)
	}
	send()
	if s := emailOf(t, inbox(t, w, w.user.ID, core.NotifyChannelReauth)[0]); !strings.HasPrefix(s, "failed: 550") {
		t.Fatalf("after a permanent failure: %s", s)
	}
	mail.failWith(nil)

	// Thirty an hour: three already went out this hour.
	for i := range 28 {
		if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyChannelReauth, fmt.Sprint("Burst ", i), "", []uuid.UUID{w.user.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := send(); n != 27 {
		t.Fatalf("sent %d of the burst, want 27 (30 in the hour)", n)
	}
	outcomes := map[string]int{}
	for _, n := range inbox(t, w, w.user.ID, core.NotifyChannelReauth) {
		if strings.HasPrefix(n.Subject, "Burst ") {
			outcomes[emailOf(t, n)]++
		}
	}
	if outcomes["sent"] != 27 || outcomes["suppressed: more than 30 notification emails in an hour"] != 1 || len(outcomes) != 2 {
		t.Fatalf("the burst's emails: %v", outcomes)
	}
}
