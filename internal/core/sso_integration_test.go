// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/oidctest"
)

// ssoWorld is a world whose org signs in through a test provider at a
// verified domain of its own.
type ssoWorld struct {
	*world
	idp    *oidctest.Provider
	domain string
}

// txt answers TXT lookups from records, by name.
func txt(records map[string][]string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, name string) ([]string, error) { return records[name], nil }
}

func newSSOWorld(t *testing.T, opts ...option) *ssoWorld {
	t.Helper()
	w := newWorld(t, opts...)
	ctx := t.Context()
	idp := oidctest.New(t)
	w.s.SSOHTTP = idp.Client()
	if err := w.s.SaveSSOConnection(ctx, w.owner, w.session, core.SSOConnectionInput{Issuer: idp.Issuer(), ClientID: idp.ClientID,
		ClientSecret: idp.ClientSecret, DefaultRole: model.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	domain := fmt.Sprintf("sso-%s.example", uuid.NewString()[:8])
	d, err := w.s.AddSSODomain(ctx, w.owner, domain)
	if err != nil {
		t.Fatal(err)
	}
	w.s.LookupTXT = txt(map[string][]string{core.SSOVerifyPrefix + domain: {"araldo-verify=" + d.Token}})
	if err := w.s.VerifySSODomain(ctx, w.owner, domain); err != nil {
		t.Fatal(err)
	}
	return &ssoWorld{world: w, idp: idp, domain: domain}
}

// signIn runs a sign-in for email, with the provider vouching for claims.
func (w *ssoWorld) signIn(t *testing.T, email string, claims map[string]any) (*core.SSOResult, error) {
	t.Helper()
	ctx := t.Context()
	authURL, state, err := w.s.BeginSSO(ctx, email, "/posts")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]any{"email": email, "email_verified": true, "name": "Ada"}
	for k, v := range claims {
		all[k] = v
	}
	gotState, authCode, err := w.idp.Authorize(authURL, all)
	if err != nil || gotState != state {
		t.Fatalf("at the provider: %v (state %q, want %q)", err, gotState, state)
	}
	return w.s.FinishSSO(ctx, state, authCode, "test", "127.0.0.1")
}

// TestSSOSetup checks connecting a provider and verifying domains: the
// issuer is checked, the secret is needed once, owners cannot be the
// default role, and a domain is verified by its TXT record for one org at
// most (ADR 0033).
func TestSSOSetup(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	idp := oidctest.New(t)
	w.s.SSOHTTP = idp.Client()
	in := core.SSOConnectionInput{Issuer: idp.Issuer(), ClientID: idp.ClientID, DefaultRole: model.RoleEditor}

	if err := w.s.SaveSSOConnection(ctx, w.owner, w.session, in); code(err) != "client_secret_required" {
		t.Fatalf("no secret the first time: %v", err)
	}
	in.ClientSecret = idp.ClientSecret
	for _, tc := range []struct {
		name string
		edit func(*core.SSOConnectionInput)
		want string
	}{
		{"not https", func(c *core.SSOConnectionInput) { c.Issuer = "http://idp.example" }, "issuer_invalid"},
		{"no discovery there", func(c *core.SSOConnectionInput) { c.Issuer = idp.Issuer() + "/elsewhere" }, "issuer_unreachable"},
		{"owner by default", func(c *core.SSOConnectionInput) { c.DefaultRole = model.RoleOwner }, "default_role_invalid"},
		{"no client ID", func(c *core.SSOConnectionInput) { c.ClientID = " " }, "client_id_invalid"},
	} {
		bad := in
		tc.edit(&bad)
		if err := w.s.SaveSSOConnection(ctx, w.owner, w.session, bad); code(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	stale := *w.session
	stale.SudoUntil = nil
	if err := w.s.SaveSSOConnection(ctx, w.owner, &stale, in); code(err) != "reauthentication_required" {
		t.Fatalf("without a recent confirmation: %v", err)
	}
	if err := w.s.SaveSSOConnection(ctx, w.owner, w.session, in); err != nil {
		t.Fatal(err)
	}
	in.ClientSecret, in.DefaultRole = "", model.RoleViewer // changing it keeps the secret
	if err := w.s.SaveSSOConnection(ctx, w.owner, w.session, in); err != nil {
		t.Fatalf("changing it without the secret: %v", err)
	}
	got, err := w.s.SSO(ctx, w.owner)
	if err != nil || got.Connection == nil || got.Connection.Issuer != idp.Issuer() || got.Connection.DefaultRole != model.RoleViewer {
		t.Fatalf("settings: %+v, %v", got, err)
	}

	// Domains.
	domain := fmt.Sprintf("sso-%s.example", uuid.NewString()[:8])
	for _, bad := range []string{"localhost", "-a.example", "a..example", "under_score.example", "ünï.example"} {
		if _, err := w.s.AddSSODomain(ctx, w.owner, bad); code(err) != "domain_invalid" {
			t.Errorf("domain %q: %v", bad, err)
		}
	}
	d, err := w.s.AddSSODomain(ctx, w.owner, " "+domain+". ")
	if err != nil || d.Domain != domain || d.Token == "" || d.VerifiedAt != nil {
		t.Fatalf("adding %s: %+v, %v", domain, d, err)
	}
	if _, err := w.s.AddSSODomain(ctx, w.owner, domain); code(err) != "domain_exists" {
		t.Fatalf("adding it twice: %v", err)
	}
	records := map[string][]string{}
	w.s.LookupTXT = txt(records)
	if err := w.s.VerifySSODomain(ctx, w.owner, domain); code(err) != "domain_unverified" {
		t.Fatalf("verifying with no record: %v", err)
	}
	records[core.SSOVerifyPrefix+domain] = []string{"v=spf1 -all", "araldo-verify=someone-elses"}
	if err := w.s.VerifySSODomain(ctx, w.owner, domain); code(err) != "domain_unverified" {
		t.Fatalf("verifying with another token: %v", err)
	}
	records[core.SSOVerifyPrefix+domain] = append(records[core.SSOVerifyPrefix+domain], "araldo-verify="+d.Token)
	if err := w.s.VerifySSODomain(ctx, w.owner, domain); err != nil {
		t.Fatalf("verifying: %v", err)
	}
	if got, _ := w.s.SSO(ctx, w.owner); len(got.Domains) != 1 || got.Domains[0].VerifiedAt == nil {
		t.Fatalf("domains: %+v", got.Domains)
	}

	// Another org can add the domain, but not verify it too, and sees
	// nothing of this org's.
	other := newWorld(t)
	other.s.LookupTXT = w.s.LookupTXT
	od, err := other.s.AddSSODomain(ctx, other.owner, domain)
	if err != nil {
		t.Fatal(err)
	}
	records[core.SSOVerifyPrefix+domain] = []string{"araldo-verify=" + od.Token}
	if err := other.s.VerifySSODomain(ctx, other.owner, domain); code(err) != "domain_taken" {
		t.Fatalf("verifying another org's domain: %v", err)
	}
	if got, err := other.s.SSO(ctx, other.owner); err != nil || got.Connection != nil || len(got.Domains) != 1 || got.Domains[0].VerifiedAt != nil {
		t.Fatalf("the other org's settings: %+v, %v", got, err)
	}
	if err := other.s.RemoveSSODomain(ctx, other.owner, domain); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.s.SSO(ctx, w.owner); len(got.Domains) != 1 {
		t.Fatalf("removing the other org's copy removed this one's: %+v", got.Domains)
	}

	// Only owners.
	viewer := w.owner
	viewer.Role = model.RoleViewer
	if _, err := w.s.SSO(ctx, viewer); err == nil {
		t.Fatal("a viewer read the settings")
	}
	if _, err := w.s.AddSSODomain(ctx, viewer, "x-"+domain); err == nil {
		t.Fatal("a viewer added a domain")
	}
}

// TestSSOSignIn checks signing in through the provider: a newcomer gets an
// account and joins with the default role, a member keeps theirs, the
// session is in the org and needs no second factor, and the provider's
// answer is refused when replayed, for someone else, for another domain or
// with an unverified email (ADR 0033).
func TestSSOSignIn(t *testing.T) {
	t.Parallel()
	w := newSSOWorld(t)
	ctx := t.Context()

	if _, _, err := w.s.BeginSSO(ctx, "ada@unknown-"+w.domain, ""); code(err) != "sso_unavailable" {
		t.Fatalf("a domain no org verified: %v", err)
	}

	email := "ada@" + w.domain
	res, err := w.signIn(t, email, nil)
	if err != nil {
		t.Fatal(err)
	}
	ss := res.Session
	if res.Next != "/posts" || ss.State != model.SessionActive || ss.SSOOrg == nil || *ss.SSOOrg != w.org.ID || ss.CurrentOrg == nil ||
		*ss.CurrentOrg != w.org.ID || !w.s.InSudo(ss) {
		t.Fatalf("the session: %+v, next %q", ss, res.Next)
	}
	if res.User.Email != email || res.User.Name != "Ada" || res.User.PasswordHash != "" {
		t.Fatalf("the new account: %+v", res.User)
	}
	m, err := w.s.Member(ctx, w.owner, res.User.ID)
	if err != nil || m.Role != model.RoleViewer {
		t.Fatalf("membership: %+v, %v", m, err)
	}
	if got, _, err := w.s.Session(ctx, res.Token); err != nil || got.ID != ss.ID {
		t.Fatalf("the session token: %v", err)
	}
	// A password cannot sign in to the account SSO made.
	if _, err := w.s.Login(ctx, email, "", "test", "127.0.0.1"); code(err) != "bad_credentials" {
		t.Fatalf("a password sign-in: %v", err)
	}

	// Again: the same account and role, even after a promotion.
	if err := w.s.SetMemberRole(ctx, w.owner, w.session, res.User.ID, model.RoleEditor); err != nil {
		t.Fatal(err)
	}
	again, err := w.signIn(t, "ADA@"+w.domain, nil)
	if err != nil || again.User.ID != res.User.ID {
		t.Fatalf("signing in again: %v", err)
	}
	if m, _ := w.s.Member(ctx, w.owner, res.User.ID); m.Role != model.RoleEditor {
		t.Fatalf("the role after signing in again: %s", m.Role)
	}

	// The provider vouches for Ada in this org only: her session through it
	// stays here, cannot change how she signs in, and a CLI token approved
	// from it works only here, though she belongs to another org too.
	elsewhere := newWorld(t)
	if _, err := elsewhere.s.AddMember(ctx, elsewhere.owner, elsewhere.session, email, model.RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.s.SwitchContext(ctx, ss, elsewhere.org.ID, false); code(err) != "resource_missing" {
		t.Fatalf("switching an SSO session to another org: %v", err)
	}
	if _, _, err := w.s.BeginPasskeyRegistration(ctx, ss); code(err) != "sso_session" {
		t.Fatalf("adding a passkey from an SSO session: %v", err)
	}
	if _, _, err := w.s.BeginTOTP(ctx, ss); code(err) != "sso_session" {
		t.Fatalf("setting up an authenticator from an SSO session: %v", err)
	}
	if err := w.s.ChangePassword(ctx, ss, "", "a brand new password"); code(err) != "sso_session" {
		t.Fatalf("setting a password from an SSO session: %v", err)
	}
	start, err := w.s.StartDevice(ctx, "laptop", false, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.DecideDevice(ctx, ss, start.UserCode, true); err != nil {
		t.Fatal(err)
	}
	plain, _, err := w.s.PollDevice(ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := w.s.AuthenticateUserToken(ctx, plain, "", "req_test"); err != nil || a.OrgID != w.org.ID {
		t.Fatalf("the SSO token with no org named: %+v, %v", a, err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, plain, elsewhere.org.Name, "req_test"); code(err) != "org_unknown" {
		t.Fatalf("the SSO token in another org: %v", err)
	}

	// Refusals.
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"for another client", map[string]any{"aud": "someone-else"}, "sso_failed"},
		{"from another issuer", map[string]any{"iss": "https://idp.example"}, "sso_failed"},
		{"for another sign-in", map[string]any{"nonce": "replayed"}, "sso_failed"},
		{"expired", map[string]any{"exp": 1000}, "sso_failed"},
		{"an unverified email", map[string]any{"email_verified": false}, "sso_email_unverified"},
		{"no email", map[string]any{"email": ""}, "sso_email_missing"},
		{"another domain", map[string]any{"email": "ada@elsewhere.example"}, "sso_domain_mismatch"},
	} {
		if _, err := w.signIn(t, "grace@"+w.domain, tc.claims); err == nil || code(err) != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	if _, err := w.s.UserByEmail(ctx, "grace@"+w.domain); err == nil {
		t.Fatal("a refused sign-in made an account")
	}

	// A code redeems only with its own sign-in's verifier, and a state is
	// used once.
	authURL, _, err := w.s.BeginSSO(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	_, authCode, err := w.idp.Authorize(authURL, map[string]any{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := w.s.BeginSSO(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.FinishSSO(ctx, other, authCode, "test", "127.0.0.1"); code(err) != "sso_failed" {
		t.Fatalf("a code with another sign-in's state: %v", err)
	}
	authURL, state, err := w.s.BeginSSO(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, authCode, err = w.idp.Authorize(authURL, map[string]any{"email": email}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.FinishSSO(ctx, state, authCode, "test", "127.0.0.1"); err != nil {
		t.Fatalf("the code with its own state: %v", err)
	}
	if _, err := w.s.FinishSSO(ctx, state, authCode, "test", "127.0.0.1"); code(err) != "sso_expired" {
		t.Fatalf("replaying the state: %v", err)
	}
}

// TestRequireSSO checks an org that requires single sign-on: it is turned
// on only from a session that came through it, and then CLI tokens work in
// it only when approved from such a session; its provider and last domain
// stay until it is turned off (ADR 0033).
func TestRequireSSO(t *testing.T) {
	t.Parallel()
	w := newSSOWorld(t)
	ctx := t.Context()

	if err := w.s.SetRequireSSO(ctx, w.owner, w.session, true); code(err) != "sso_session_required" {
		t.Fatalf("turning it on from a password session: %v", err)
	}
	res, err := w.signIn(t, "ada@"+w.domain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.SetMemberRole(ctx, w.owner, w.session, res.User.ID, model.RoleOwner); err != nil {
		t.Fatal(err)
	}
	ada, _, err := w.s.MemberActor(ctx, res.User.ID, w.org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.SetRequireSSO(ctx, ada, res.Session, true); err != nil {
		t.Fatalf("turning it on through SSO: %v", err)
	}
	if o, err := w.s.Org(ctx, w.owner); err != nil || !core.ViewOrg(o).RequireSSO {
		t.Fatalf("the org once it is required: %v", err)
	}

	// CLI tokens: one approved from the password session is refused, one
	// from the SSO session works.
	token := func(ss *model.Session) string {
		t.Helper()
		start, err := w.s.StartDevice(ctx, "laptop", false, "203.0.113.7")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.s.DecideDevice(ctx, ss, start.UserCode, true); err != nil {
			t.Fatal(err)
		}
		plain, _, err := w.s.PollDevice(ctx, start.DeviceCode)
		if err != nil {
			t.Fatal(err)
		}
		return plain
	}
	if _, err := w.s.AuthenticateUserToken(ctx, token(w.session), w.org.Name, "req_test"); code(err) != "sso_required" {
		t.Fatalf("a token from a password session: %v", err)
	}
	if a, err := w.s.AuthenticateUserToken(ctx, token(res.Session), w.org.Name, "req_test"); err != nil || a.UserID == nil || *a.UserID != res.User.ID {
		t.Fatalf("a token from the SSO session: %+v, %v", a, err)
	}

	// What it depends on stays.
	if err := w.s.RemoveSSODomain(ctx, w.owner, w.domain); code(err) != "sso_required" {
		t.Fatalf("removing the last domain: %v", err)
	}
	if err := w.s.DeleteSSOConnection(ctx, w.owner, w.session); code(err) != "sso_required" {
		t.Fatalf("disconnecting: %v", err)
	}

	// Turning it off needs a recent confirmation.
	stale := *w.session
	stale.SudoUntil = nil
	if err := w.s.SetRequireSSO(ctx, w.owner, &stale, false); code(err) != "reauthentication_required" {
		t.Fatalf("turning it off without a recent confirmation: %v", err)
	}
	if err := w.s.SetRequireSSO(ctx, w.owner, w.session, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateUserToken(ctx, token(w.session), w.org.Name, "req_test"); err != nil {
		t.Fatalf("a password session's token once it is off: %v", err)
	}
	if err := w.s.DeleteSSOConnection(ctx, w.owner, w.session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.BeginSSO(ctx, "ada@"+w.domain, ""); code(err) != "sso_unavailable" {
		t.Fatalf("signing in once disconnected: %v", err)
	}
}
