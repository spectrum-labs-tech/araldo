// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestAdministrationIsTheOperators checks what `araldo admin` does over
// the operator API (ADR 0035) is refused to a member, even an owner, and
// works for the operator.
func TestAdministrationIsTheOperators(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	_, op := operatorKey(t, w.s)
	email := fmt.Sprintf("made-%s@example.com", uuid.NewString()[:8])

	refused := []struct {
		name string
		call func() error
	}{
		{"creating an account", func() error {
			_, err := w.s.CreateUserAsOperator(ctx, w.owner, email, "", "correct horse battery")
			return err
		}},
		{"finding an account", func() error { _, err := w.s.OperatorUser(ctx, w.owner, w.user.Email); return err }},
		{"resetting a password", func() error {
			_, err := w.s.ResetUserPassword(ctx, w.owner, w.user.ID, "another long password")
			return err
		}},
		{"reading master keys", func() error { _, err := w.s.MasterKeyUsage(ctx, w.owner); return err }},
		{"rewrapping", func() error { _, err := w.s.RewrapDataKeys(ctx, w.owner, 10); return err }},
	}
	for _, tt := range refused {
		if err := tt.call(); kind(err) != apperr.KindForbidden {
			t.Errorf("an owner %s: %v", tt.name, err)
		}
	}

	u, err := w.s.CreateUserAsOperator(ctx, op, email, "Made", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreateUserAsOperator(ctx, op, email, "", "correct horse battery"); code(err) != "email_taken" {
		t.Fatalf("the same email again: %v", err)
	}
	if _, err := w.s.CreateUserAsOperator(ctx, op, "short-"+email, "", "short"); code(err) != "password_invalid" {
		t.Fatalf("a short password: %v", err)
	}
	if found, err := w.s.OperatorUser(ctx, op, email); err != nil || found.ID != u.ID {
		t.Fatalf("finding the account: %+v, %v", found, err)
	}
	if _, err := w.s.ResetUserPassword(ctx, op, u.ID, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Login(ctx, email, "a brand new password", "test", "127.0.0.1"); err != nil {
		t.Fatalf("signing in with the new password: %v", err)
	}
	if _, err := w.s.ResetUserPassword(ctx, op, uuid.New(), "a brand new password"); kind(err) != apperr.KindNotFound {
		t.Fatalf("resetting nobody's password: %v", err)
	}

	use, err := w.s.MasterKeyUsage(ctx, op)
	if err != nil || len(use) == 0 || !use[0].Primary || !use[0].Configured {
		t.Fatalf("master keys: %+v, %v", use, err)
	}
	// Every test keyring has the one master key, so nothing is left to
	// move (keys from other local runs, under keys not configured here,
	// are counted, not touched).
	if r, err := w.s.RewrapDataKeys(ctx, op, 0); err != nil || r.Rewrapped != 0 || r.Remaining != 0 {
		t.Fatalf("rewrap: %+v, %v", r, err)
	}
	for _, n := range []int{-1, core.MaxRewrapBatch + 1} {
		if _, err := w.s.RewrapDataKeys(ctx, op, n); code(err) != "batch_size_invalid" {
			t.Errorf("a batch of %d: %v", n, err)
		}
	}
}

// TestAnOperatorKeyCannotRevokeItself keeps an install from losing its
// last way into the operator API (ADR 0035).
func TestAnOperatorKeyCannotRevokeItself(t *testing.T) {
	t.Parallel()
	s := service(t)
	ctx := t.Context()
	_, a := operatorKey(t, s)
	plainB, b := operatorKey(t, s)
	if err := s.RevokeOperatorKey(ctx, b, *b.OperatorKeyID); code(err) != "operator_key_self" {
		t.Fatalf("a key revoking itself: %v", err)
	}
	if err := s.RevokeOperatorKey(ctx, a, *b.OperatorKeyID); err != nil {
		t.Fatalf("another key revoking it: %v", err)
	}
	if _, err := s.AuthenticateOperatorKey(ctx, plainB, ""); code(err) != "operator_key_revoked" {
		t.Fatalf("the revoked key: %v", err)
	}
}

// TestOperatorChangesOwnersSettings checks the operator can change an
// org's two-factor and single sign-on requirements, which are the owners'
// (ADR 0033), with the owners' rules, and finds orgs by name (ADR 0035).
func TestOperatorChangesOwnersSettings(t *testing.T) {
	t.Parallel()
	w, other := newWorld(t), newWorld(t)
	ctx := t.Context()
	_, op := operatorKey(t, w.s)
	in, _, err := w.s.InOrg(ctx, op, w.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	on, off, notRenamed := true, false, "Not renamed"
	o, err := w.s.ChangeOrg(ctx, in, core.OrgChange{RequireMFA: &on})
	if err != nil || !o.RequireMFA {
		t.Fatalf("requiring two-factor: %+v, %v", o, err)
	}
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{RequireSSO: &on, Name: &notRenamed}); code(err) != "sso_not_connected" {
		t.Fatalf("requiring single sign-on with no provider: %v", err)
	}
	if o, err = w.s.ChangeOrg(ctx, in, core.OrgChange{RequireMFA: &off, RequireSSO: &off}); err != nil || o.RequireMFA || o.RequireSSO || o.Name != w.org.Name {
		t.Fatalf("turning both off (and the refused rename left undone): %+v, %v", o, err)
	}
	// The other org is untouched.
	if got, err := other.s.Org(ctx, other.owner); err != nil || got.RequireMFA {
		t.Fatalf("the other org: %+v, %v", got, err)
	}

	found, _, err := w.s.OperatedOrgs(ctx, op, store.Page{}, core.OrgFilter{Name: w.org.Name})
	if err != nil || len(found) != 1 || found[0].ID != w.org.ID {
		t.Fatalf("by name: %v, %v", found, err)
	}
	if none, _, err := w.s.OperatedOrgs(ctx, op, store.Page{}, core.OrgFilter{Name: "no org " + uuid.NewString()}); err != nil || len(none) != 0 {
		t.Fatalf("an unused name: %v, %v", none, err)
	}

	// Members change as the operator, and the audit log says so.
	newcomer := fmt.Sprintf("newcomer-%s@example.com", uuid.NewString()[:8])
	if _, err := w.s.AddMember(ctx, in, nil, newcomer, model.RoleEditor, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	events, _, err := w.s.AuditEvents(ctx, w.owner, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, e := range events {
		if e.Action == "member.add" {
			seen = e.ActorUser == nil && e.Detail["operator_key"] == id.Format(id.OperatorKey, *op.OperatorKeyID)
		}
	}
	if !seen {
		t.Fatalf("member.add is not audited as the operator key: %+v", events)
	}
}
