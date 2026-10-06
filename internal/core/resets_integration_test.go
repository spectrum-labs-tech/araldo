// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// outbox is a mail sender that keeps what it is given.
type outbox struct {
	mu   sync.Mutex
	sent []smtpmail.Message
}

func (o *outbox) Send(_ context.Context, m smtpmail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, m)
	return nil
}

// to lists what was sent to an address.
func (o *outbox) to(addr string) []smtpmail.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []smtpmail.Message
	for _, m := range o.sent {
		if strings.EqualFold(m.To, addr) {
			out = append(out, m)
		}
	}
	return out
}

func withMail(o *outbox) option {
	return func(cfg *core.Config, _ *[]platform.Adapter) { cfg.Mail = o }
}

var resetLink = regexp.MustCompile(`https://araldo\.test/login/reset/([A-Za-z0-9_-]+)`)

// TestPasswordResetByEmail checks the reset link: sent only to an account
// that exists, at most three an hour, working once and for 30 minutes, and
// signing the person out everywhere (ADR 0034).
func TestPasswordResetByEmail(t *testing.T) {
	t.Parallel()
	mail := &outbox{}
	w := newWorld(t, withMail(mail))
	ctx := t.Context()

	if err := w.s.RequestPasswordReset(ctx, fmt.Sprintf("nobody-%s@example.com", uuid.NewString()[:8]), "203.0.113.7"); err != nil {
		t.Fatalf("an unknown address: %v", err)
	}
	if err := w.s.RequestPasswordReset(ctx, "not an address", ""); code(err) != "email_invalid" {
		t.Fatalf("a malformed address: %v", err)
	}
	if err := w.s.RequestPasswordReset(ctx, strings.ToUpper(w.user.Email), "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	sent := mail.to(w.user.Email)
	if len(sent) != 1 || !strings.Contains(sent[0].Subject, "Reset") {
		t.Fatalf("sent %d messages: %+v", len(sent), sent)
	}
	m := resetLink.FindStringSubmatch(sent[0].Text)
	if m == nil || !strings.Contains(sent[0].HTML, m[0]) {
		t.Fatalf("no link in the message:\n%s", sent[0].Text)
	}
	token := m[1]
	if err := w.s.CheckPasswordReset(ctx, token); err != nil {
		t.Fatalf("the fresh link: %v", err)
	}

	// A weak password is refused without spending the link.
	if err := w.s.ResetPasswordWithLink(ctx, token, "short"); code(err) != "password_invalid" {
		t.Fatalf("a weak password: %v", err)
	}
	if err := w.s.ResetPasswordWithLink(ctx, token, "a whole new password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.Session(ctx, w.loginToken); err == nil {
		t.Fatal("a session survived the reset")
	}
	if _, err := w.s.Login(ctx, w.user.Email, "a whole new password", "test", "127.0.0.1"); err != nil {
		t.Fatalf("signing in with the new password: %v", err)
	}
	if err := w.s.ResetPasswordWithLink(ctx, token, "yet another password"); code(err) != "reset_link_invalid" {
		t.Fatalf("using the link again: %v", err)
	}

	// Three an hour.
	for range 4 {
		if err := w.s.RequestPasswordReset(ctx, w.user.Email, ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(mail.to(w.user.Email)); n != 4 {
		t.Fatalf("%d messages in all, want the first and three more", n)
	}

	// Thirty minutes.
	last := mail.to(w.user.Email)[3]
	stale := resetLink.FindStringSubmatch(last.Text)[1]
	w.s.Now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if err := w.s.CheckPasswordReset(ctx, stale); code(err) != "reset_link_invalid" {
		t.Fatalf("an expired link: %v", err)
	}
	if err := w.s.ResetPasswordWithLink(ctx, stale, "a whole new password 2"); code(err) != "reset_link_invalid" {
		t.Fatalf("using an expired link: %v", err)
	}
}

// TestPasswordResetWithoutMail checks a server that sends no email says so.
func TestPasswordResetWithoutMail(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if w.s.MailEnabled() {
		t.Fatal("mail enabled with no sender")
	}
	if err := w.s.RequestPasswordReset(t.Context(), w.user.Email, ""); code(err) != "mail_unavailable" {
		t.Fatalf("asking for a reset: %v", err)
	}
}

// TestPasswordResetForSSO checks an address at a domain an org signs in
// with by single sign-on gets a link to sign in that way, not a reset.
func TestPasswordResetForSSO(t *testing.T) {
	t.Parallel()
	mail := &outbox{}
	w := newSSOWorld(t, withMail(mail))
	res, err := w.signIn(t, "ada@"+w.domain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.RequestPasswordReset(t.Context(), res.User.Email, ""); err != nil {
		t.Fatal(err)
	}
	sent := mail.to(res.User.Email)
	if len(sent) != 1 || resetLink.MatchString(sent[0].Text) || !strings.Contains(sent[0].Text, "/login/sso?email=") {
		t.Fatalf("sent: %+v", sent)
	}
}
