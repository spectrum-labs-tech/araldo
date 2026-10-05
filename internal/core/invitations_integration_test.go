// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"net/url"
	"path"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// tokenOf is the token at the end of an invitation link.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || path.Dir(u.Path) != "/invite" {
		t.Fatalf("invitation link %q", link)
	}
	return path.Base(u.Path)
}

// TestInvitations checks that people join an org only by accepting an
// invitation, that inviting says nothing about who has an account, and
// that a link works once, for its invitee, until it expires.
func TestInvitations(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	code := func(err error) string { return apperr.As(err).Code }

	// Someone with an account in another org, and someone with none: the
	// inviter sees the same thing for both.
	other := newWorld(t)
	newcomer := fmt.Sprintf("new-%s@example.com", uuid.NewString()[:8])
	existingLink, inv, err := w.s.InviteMember(ctx, w.owner, nil, other.user.Email, model.RoleEditor)
	if err != nil || inv.Role != model.RoleEditor {
		t.Fatalf("inviting someone with an account: %v", err)
	}
	newLink, _, err := w.s.InviteMember(ctx, w.owner, nil, newcomer, model.RoleViewer)
	if err != nil {
		t.Fatalf("inviting someone without one: %v", err)
	}
	// Inviting adds nobody.
	if _, err := w.s.Member(ctx, w.owner, other.user.ID); apperr.As(err).Kind != apperr.KindNotFound {
		t.Fatalf("an invited person is a member before accepting: %v", err)
	}
	if list, err := w.s.Invitations(ctx, w.owner); err != nil || len(list) != 2 {
		t.Fatalf("open invitations: %d, %v", len(list), err)
	}

	// Only the invitee can accept, once.
	if _, err := w.s.AcceptInvitation(ctx, tokenOf(t, existingLink), w.user, "req_test"); apperr.As(err).Kind != apperr.KindForbidden {
		t.Fatalf("someone else accepting: %v", err)
	}
	if _, err := w.s.AcceptInvitation(ctx, tokenOf(t, existingLink), other.user, "req_test"); err != nil {
		t.Fatalf("the invitee accepting: %v", err)
	}
	if m, err := w.s.Member(ctx, w.owner, other.user.ID); err != nil || m.Role != model.RoleEditor {
		t.Fatalf("after accepting: %+v, %v", m, err)
	}
	if _, err := w.s.AcceptInvitation(ctx, tokenOf(t, existingLink), other.user, "req_test"); code(err) != "invitation_invalid" {
		t.Fatalf("accepting twice: %v", err)
	}

	// Without an account, accepting makes one with the invitee's password;
	// with one, it asks them to sign in.
	if _, _, err := w.s.AcceptInvitationNewAccount(ctx, tokenOf(t, newLink), "New Person", "short", "req_test"); apperr.As(err).Kind != apperr.KindInvalid {
		t.Fatalf("a weak password: %v", err)
	}
	u, _, err := w.s.AcceptInvitationNewAccount(ctx, tokenOf(t, newLink), "New Person", "a long enough password", "req_test")
	if err != nil {
		t.Fatalf("accepting with a new account: %v", err)
	}
	if m, err := w.s.Member(ctx, w.owner, u.ID); err != nil || m.Role != model.RoleViewer {
		t.Fatalf("the new account's membership: %+v, %v", m, err)
	}
	againLink, _, err := w.s.InviteMember(ctx, other.owner, nil, newcomer, model.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.AcceptInvitationNewAccount(ctx, tokenOf(t, againLink), "x", "another long password", "req_test"); code(err) != "account_exists" {
		t.Fatalf("a new account for an email that has one: %v", err)
	}

	// Revoked and expired links stop working.
	revoked, rinv, err := w.s.InviteMember(ctx, w.owner, nil, "revoke-"+newcomer, model.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.RevokeInvitation(ctx, w.owner, rinv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Invitation(ctx, tokenOf(t, revoked)); code(err) != "invitation_invalid" {
		t.Fatalf("a revoked link: %v", err)
	}
	expiring, _, err := w.s.InviteMember(ctx, w.owner, nil, "late-"+newcomer, model.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(core.InvitationTTL + time.Hour) }
	if _, err := w.s.Invitation(ctx, tokenOf(t, expiring)); code(err) != "invitation_invalid" {
		t.Fatalf("an expired link: %v", err)
	}
	w.s.Now = time.Now

	// Inviting an owner takes what making one does.
	stale := *w.session
	stale.SudoUntil = nil
	if _, _, err := w.s.InviteMember(ctx, w.owner, &stale, "owner-"+newcomer, model.RoleOwner); code(err) != "reauthentication_required" {
		t.Fatalf("inviting an owner without sudo: %v", err)
	}
	if _, _, err := w.s.InviteMember(ctx, w.owner, w.session, "owner-"+newcomer, model.RoleOwner); err != nil {
		t.Fatalf("inviting an owner in sudo mode: %v", err)
	}
}
