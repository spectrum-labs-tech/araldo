// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// sentMail is a mail sender that keeps what it is given.
type sentMail struct {
	mu   sync.Mutex
	sent []smtpmail.Message
}

func (m *sentMail) Send(_ context.Context, msg smtpmail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *sentMail) to(addr string) []smtpmail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []smtpmail.Message
	for _, s := range m.sent {
		if s.To == addr {
			out = append(out, s)
		}
	}
	return out
}

// TestInvitationsEmailedOverTheAPI checks send_email emails an
// invitation's link from a person's token and from the operator API, says
// so in emailed, and that without it nothing is sent (ADR 0034).
func TestInvitationsEmailedOverTheAPI(t *testing.T) {
	t.Parallel()
	mail := &sentMail{}
	c := newClient(t, func(cfg *core.Config) { cfg.Mail = mail })
	as := tokenFor(t, c)
	invite := func(body map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		status, got := c.do(http.MethodPost, "/v1/invitations", "application/json", b, as)
		if status != http.StatusCreated {
			t.Fatalf("inviting: %d %v", status, got)
		}
		return got
	}

	emailed := fmt.Sprintf("emailed-%s@example.com", uuid.NewString()[:8])
	got := invite(map[string]any{"email": emailed, "role": "editor", "send_email": true})
	sent := mail.to(emailed)
	if got["emailed"] != true || len(sent) != 1 || !strings.Contains(sent[0].Text, got["url"].(string)) || !strings.Contains(sent[0].Text, "invited you") {
		t.Fatalf("send_email: %v, sent %+v", got, sent)
	}
	quiet := fmt.Sprintf("quiet-%s@example.com", uuid.NewString()[:8])
	if got := invite(map[string]any{"email": quiet, "role": "editor"}); got["emailed"] != nil || len(mail.to(quiet)) != 0 {
		t.Fatalf("without send_email: %v, sent %d", got, len(mail.to(quiet)))
	}

	// The operator's first-owner invitation.
	plain, _, err := c.s.CreateOperatorKey(t.Context(), core.InstallOperator("test"), "billing "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	op := *c
	op.key = plain
	owner := fmt.Sprintf("owner-%s@example.com", uuid.NewString()[:8])
	b, _ := json.Marshal(map[string]any{"name": "Hosted " + owner, "owner_email": owner, "external_ref": "cus_" + uuid.NewString(), "send_email": true})
	status, created := op.do(http.MethodPost, "/v1/operator/orgs", "application/json", b, nil)
	inv, _ := created["owner_invitation"].(map[string]any)
	sent = mail.to(owner)
	if status != http.StatusCreated || inv["emailed"] != true || len(sent) != 1 || !strings.Contains(sent[0].Text, "You are invited to join") {
		t.Fatalf("the operator's owner invitation: %d %v, sent %+v", status, created, sent)
	}
}

// TestInvitationEmailWithoutMail checks a server that sends no email says
// so, and the invitation still stands with its link.
func TestInvitationEmailWithoutMail(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	b, _ := json.Marshal(map[string]any{"email": fmt.Sprintf("x-%s@example.com", uuid.NewString()[:8]), "role": "viewer", "send_email": true})
	status, got := c.do(http.MethodPost, "/v1/invitations", "application/json", b, tokenFor(t, c))
	if status != http.StatusCreated || got["emailed"] != false || got["url"] == "" {
		t.Fatalf("send_email without mail: %d %v", status, got)
	}
}
