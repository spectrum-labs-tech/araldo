// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestDeviceSignIn drives the CLI's sign-in end to end: the CLI starts it
// and polls, the person approves the code in sudo mode, the CLI gets a
// token once, and the token acts as the person in the org a request names,
// in its mode, until revoked.
func TestDeviceSignIn(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	code := func(err error) string { return apperr.As(err).Code }

	start, err := w.s.StartDevice(ctx, "araldo CLI on laptop", true, "203.0.113.7")
	if err != nil || authn.NormalizeUserCode(start.UserCode) != start.UserCode || start.Interval != 5 || start.VerificationURI != "https://araldo.test/device" {
		t.Fatalf("StartDevice: %+v, %v", start, err)
	}
	now := time.Now()
	w.s.Now = func() time.Time { return now }
	if _, _, err := w.s.PollDevice(ctx, start.DeviceCode); code(err) != "authorization_pending" {
		t.Fatalf("polling before approval: %v", err)
	}
	if _, _, err := w.s.PollDevice(ctx, start.DeviceCode); code(err) != "slow_down" {
		t.Fatalf("polling again at once: %v", err)
	}

	// The person finds it by the code as typed, and approving needs sudo.
	d, err := w.s.PendingDevice(ctx, " "+start.UserCode[:4]+start.UserCode[5:]+" ")
	if err != nil || d.DeviceName != "araldo CLI on laptop" || !d.Livemode || d.ClientIP != "203.0.113.7" {
		t.Fatalf("PendingDevice: %+v, %v", d, err)
	}
	stale := *w.session
	stale.SudoUntil = nil
	if _, err := w.s.DecideDevice(ctx, &stale, start.UserCode, true); code(err) != "reauthentication_required" {
		t.Fatalf("approving without sudo: %v", err)
	}
	if _, err := w.s.DecideDevice(ctx, w.session, start.UserCode, true); err != nil {
		t.Fatalf("approving: %v", err)
	}
	if _, err := w.s.PendingDevice(ctx, start.UserCode); code(err) != "device_code_unknown" {
		t.Fatalf("the code after approval: %v", err)
	}
	now = now.Add(20 * time.Second) // past the slowed-down interval
	plain, tok, err := w.s.PollDevice(ctx, start.DeviceCode)
	if err != nil || !authn.IsUserToken(plain) || !tok.Livemode || tok.UserID != w.user.ID {
		t.Fatalf("the token: %q %+v, %v", plain, tok, err)
	}
	now = now.Add(20 * time.Second)
	if _, _, err := w.s.PollDevice(ctx, start.DeviceCode); code(err) != "invalid_grant" {
		t.Fatalf("polling after the token was issued: %v", err)
	}
	w.s.Now = time.Now

	// The token is the person, as a member of the org named, in live mode.
	a, err := w.s.AuthenticateUserToken(ctx, plain, "", "req_test")
	if err != nil || a.OrgID != w.org.ID || !a.Livemode || a.Role != model.RoleOwner || a.TokenID == nil || a.IsKey() {
		t.Fatalf("the token's actor: %+v, %v", a, err)
	}
	if a2, err := w.s.AuthenticateUserToken(ctx, plain, id.Format(id.Org, w.org.ID), "req_test"); err != nil || a2.OrgID != w.org.ID {
		t.Fatalf("naming the org by ID: %v", err)
	}
	// A second org: now the request must say which.
	other := newWorld(t)
	if _, err := other.s.AddMember(ctx, other.owner, other.session, w.user.Email, model.RoleViewer, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, plain, "", "req_test"); code(err) != "org_required" {
		t.Fatalf("no org, with two: %v", err)
	}
	if a3, err := w.s.AuthenticateUserToken(ctx, plain, other.org.Name, "req_test"); err != nil || a3.OrgID != other.org.ID || a3.Role != model.RoleViewer {
		t.Fatalf("naming the other org: %+v, %v", a3, err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, plain, "Not mine", "req_test"); code(err) != "org_unknown" {
		t.Fatalf("an org they are not in: %v", err)
	}
	// Sudo-mode actions stay in the dashboard.
	if _, _, err := w.s.CreateAPIKey(ctx, a, nil, core.APIKeyInput{Name: "from the CLI"}); apperr.As(err).Kind != apperr.KindForbidden {
		t.Fatalf("creating a key with a token: %v", err)
	}
	me, err := w.s.Me(ctx, a)
	if err != nil || me.User == nil || me.User.Email != w.user.Email || me.UserToken == nil || me.Role != model.RoleOwner {
		t.Fatalf("Me: %+v, %v", me, err)
	}

	// Revoked, it stops at once; and reset passwords revoke every token.
	if err := w.s.RevokeOwnToken(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, plain, "", "req_test"); code(err) != "user_token_invalid" {
		t.Fatalf("a revoked token: %v", err)
	}
	second := approved(t, w, false)
	if err := w.s.ResetPassword(ctx, w.user.Email, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, second, id.Format(id.Org, w.org.ID), "req_test"); code(err) != "user_token_invalid" {
		t.Fatalf("a token after a password reset: %v", err)
	}
}

// TestDeviceSignInDeniedOrLate checks the ways a sign-in ends without a token.
func TestDeviceSignInDeniedOrLate(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	code := func(err error) string { return apperr.As(err).Code }

	denied, err := w.s.StartDevice(ctx, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.DecideDevice(ctx, w.session, denied.UserCode, false); err != nil {
		t.Fatalf("denying: %v", err)
	}
	if _, _, err := w.s.PollDevice(ctx, denied.DeviceCode); code(err) != "access_denied" {
		t.Fatalf("after a denial: %v", err)
	}

	late, err := w.s.StartDevice(ctx, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(core.DeviceCodeTTL + time.Minute) }
	if _, _, err := w.s.PollDevice(ctx, late.DeviceCode); code(err) != "expired_token" {
		t.Fatalf("an expired sign-in: %v", err)
	}
	if _, err := w.s.PendingDevice(ctx, late.UserCode); code(err) != "device_code_unknown" {
		t.Fatalf("approving an expired code: %v", err)
	}
	w.s.Now = time.Now
	if _, _, err := w.s.PollDevice(ctx, "not a device code"); code(err) != "invalid_grant" {
		t.Fatalf("an unknown device code: %v", err)
	}

	// An org that requires two-factor refuses a token of someone without it.
	tok := approved(t, w, false)
	if err := w.s.UpdateOrg(ctx, w.owner, w.session, w.org.Name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, tok, "", "req_test"); err != nil {
		t.Fatalf("before requiring 2FA: %v", err)
	}
	// The owner has no 2FA, so the operator turns the requirement on.
	operator, _, err := w.s.OperatorActor(ctx, id.Format(id.Org, w.org.ID), false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.UpdateOrg(ctx, operator, nil, w.org.Name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, tok, "", "req_test"); apperr.As(err).Kind != apperr.KindForbidden {
		t.Fatalf("an org requiring 2FA, the person without it: %v", err)
	}
}

// approved signs a device in for w's user and returns its token.
func approved(t *testing.T, w *world, live bool) string {
	t.Helper()
	ctx := t.Context()
	start, err := w.s.StartDevice(ctx, "test device", live, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.DecideDevice(ctx, w.session, start.UserCode, true); err != nil {
		t.Fatal(err)
	}
	plain, _, err := w.s.PollDevice(ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}
