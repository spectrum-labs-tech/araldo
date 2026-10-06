// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// failingSender refuses every message.
type failingSender struct{}

func (failingSender) Send(context.Context, smtpmail.Message) error {
	return errors.New("connection refused")
}

// TestInvitationsByEmail checks an invitation is emailed to the person
// invited, with the link the page shows, and that when sending fails the
// page says so and still shows the link to share (ADR 0034).
func TestInvitationsByEmail(t *testing.T) {
	t.Parallel()
	invite := func(t *testing.T, d *dash) (string, string) {
		t.Helper()
		email := fmt.Sprintf("newcomer-%s@example.com", uuid.NewString()[:8])
		form := url.Values{"csrf": {d.login.Session.CSRFToken}, "email": {email}, "role": {"editor"}}
		r := httptest.NewRequest(http.MethodPost, "/org/invitations", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := d.send(r)
		if rec.Code != http.StatusOK {
			t.Fatalf("inviting: %d", rec.Code)
		}
		return email, rec.Body.String()
	}
	link := regexp.MustCompile(`https://araldo\.test/invite/[A-Za-z0-9_-]+`)

	t.Run("sent", func(t *testing.T) {
		t.Parallel()
		box := make(mailbox, 1)
		d := newDashWith(t, func(cfg *core.Config) { cfg.Mail = box })
		email, page := invite(t, d)
		var msg smtpmail.Message
		select {
		case msg = <-box:
		case <-time.After(10 * time.Second):
			t.Fatal("no invitation email")
		}
		shown := link.FindString(page)
		if msg.To != email || shown == "" || link.FindString(msg.Text) != shown || !strings.Contains(page, "We emailed") ||
			!strings.Contains(msg.Text, d.login.User.Email) {
			t.Fatalf("the email to %s:\n%s\nthe page shows %q", msg.To, msg.Text, shown)
		}
	})
	t.Run("not sent", func(t *testing.T) {
		t.Parallel()
		d := newDashWith(t, func(cfg *core.Config) { cfg.Mail = failingSender{} })
		_, page := invite(t, d)
		if !strings.Contains(page, "We could not email it") || link.FindString(page) == "" || strings.Contains(page, "We emailed") {
			t.Fatalf("the page after a failed send:\n%s", page)
		}
	})
}
