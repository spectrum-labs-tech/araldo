// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/webauthntest"
)

// addPasskey registers a passkey for w's owner on device, through ss.
func addPasskey(t *testing.T, w *world, ss *model.Session, device *webauthntest.Authenticator) *model.Passkey {
	t.Helper()
	ctx := t.Context()
	options, token, err := w.s.BeginPasskeyRegistration(ctx, ss)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := device.Create(options)
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.FinishPasskeyRegistration(ctx, ss, token, "Laptop", resp)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPasskeys checks a passkey signs in alone, serves as the second factor
// of a password sign-in and as a confirmation, meets an org's two-factor
// requirement, and is refused when replayed, misdirected or someone
// else's (ADR 0007).
func TestPasskeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	device := &webauthntest.Authenticator{RPID: "araldo.test", Origin: "https://araldo.test"}

	stale := *w.session
	stale.SudoUntil = nil
	if _, _, err := w.s.BeginPasskeyRegistration(ctx, &stale); code(err) != "reauthentication_required" {
		t.Fatalf("adding a passkey without a recent confirmation: %v", err)
	}
	p := addPasskey(t, w, w.session, device)
	if keys, err := w.s.Passkeys(ctx, w.user.ID); err != nil || len(keys) != 1 || keys[0].ID != p.ID || keys[0].Name != "Laptop" {
		t.Fatalf("passkeys: %v, %v", keys, err)
	}

	// Signing in with the passkey alone: active, and confirmed.
	options, token, err := w.s.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := device.Get(options)
	if err != nil {
		t.Fatal(err)
	}
	login, err := w.s.FinishPasskeyLogin(ctx, token, resp, "test", "127.0.0.1")
	if err != nil || login.NeedsMFA || login.Session.State != model.SessionActive || !w.s.InSudo(login.Session) || login.User.ID != w.user.ID {
		t.Fatalf("a passkey sign-in: %+v, %v", login, err)
	}
	if _, err := w.s.FinishPasskeyLogin(ctx, token, resp, "test", "127.0.0.1"); code(err) != "passkey_expired" {
		t.Fatalf("the same answer again: %v", err)
	}
	_, other, err := w.s.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.FinishPasskeyLogin(ctx, other, resp, "test", "127.0.0.1"); code(err) != "passkey_invalid" {
		t.Fatalf("an answer to another challenge: %v", err)
	}

	// A password sign-in now waits for the second factor: a code will not
	// do (there is no authenticator app), the passkey will.
	pw, err := w.s.Login(ctx, w.user.Email, "correct horse battery", "test", "127.0.0.1")
	if err != nil || !pw.NeedsMFA {
		t.Fatalf("a password sign-in with a passkey: %+v, %v", pw, err)
	}
	if err := w.s.VerifySecondFactor(ctx, pw.Session, "123456"); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("a code where only a passkey will do: %v", err)
	}
	options, token, err = w.s.BeginPasskeyAssertion(ctx, pw.Session)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err = device.Get(options); err != nil {
		t.Fatal(err)
	}
	if err := w.s.FinishPasskeyAssertion(ctx, pw.Session, token, resp); err != nil {
		t.Fatal(err)
	}
	if ss, _, err := w.s.Session(ctx, pw.Token); err != nil || ss.State != model.SessionActive {
		t.Fatalf("after the passkey: %+v, %v", ss, err)
	}

	// Confirming: the password alone is not enough; the passkey is.
	if err := w.s.Reauthenticate(ctx, &stale, "correct horse battery", ""); code(err) != "passkey_required" {
		t.Fatalf("confirming with only the password: %v", err)
	}
	options, token, err = w.s.BeginPasskeyAssertion(ctx, &stale)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err = device.Get(options); err != nil {
		t.Fatal(err)
	}
	if err := w.s.FinishPasskeyAssertion(ctx, &stale, token, resp); err != nil {
		t.Fatalf("confirming with the passkey: %v", err)
	}

	// Another person's passkey proves nothing for this account.
	someone := newWorld(t)
	theirs := &webauthntest.Authenticator{RPID: "araldo.test", Origin: "https://araldo.test"}
	addPasskey(t, someone, someone.session, theirs)
	_, token, err = w.s.BeginPasskeyAssertion(ctx, w.session)
	if err != nil {
		t.Fatal(err)
	}
	options2, _, err := someone.s.BeginPasskeyAssertion(ctx, someone.session)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err = theirs.Get(options2); err != nil {
		t.Fatal(err)
	}
	if err := w.s.FinishPasskeyAssertion(ctx, w.session, token, resp); code(err) != "passkey_invalid" {
		t.Fatalf("someone else's passkey: %v", err)
	}

	// It meets an org's two-factor requirement, so it cannot be the last
	// second factor removed while that holds.
	owner := memberActor(t, w, false)
	if err := w.s.UpdateOrg(ctx, owner, login.Session, w.org.Name, true); err != nil {
		t.Fatalf("requiring two-factor with a passkey: %v", err)
	}
	if err := w.s.DeletePasskey(ctx, login.Session, p.ID); kind(err) != apperr.KindForbidden {
		t.Fatalf("removing the last second factor where it is required: %v", err)
	}
	if err := w.s.UpdateOrg(ctx, owner, login.Session, w.org.Name, false); err != nil {
		t.Fatal(err)
	}
	if err := w.s.DeletePasskey(ctx, login.Session, p.ID); err != nil {
		t.Fatal(err)
	}
	if l, err := w.s.Login(ctx, w.user.Email, "correct horse battery", "test", "127.0.0.1"); err != nil || l.NeedsMFA {
		t.Fatalf("a password sign-in with no second factor left: %+v, %v", l, err)
	}
	if _, _, err := w.s.BeginPasskeyAssertion(ctx, w.session); kind(err) != apperr.KindInvalid {
		t.Fatalf("a passkey ceremony with none: %v", err)
	}
}
