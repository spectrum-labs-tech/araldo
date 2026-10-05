// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"bytes"
	"context"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeOAuth is a platform whose channels connect with OAuth (it stands in
// for Threads). Codes decide what the sign-in returns: "one", "many", or
// "revoked" (an account whose token cannot be renewed).
type fakeOAuth struct{}

func (fakeOAuth) Provider() platform.Provider { return platform.Threads }
func (fakeOAuth) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Threads)
	return r
}
func (fakeOAuth) Fields() []platform.Field { return nil }
func (fakeOAuth) Idempotent() bool         { return false }
func (fakeOAuth) Verify(context.Context, platform.Credentials) (platform.Account, error) {
	return platform.Account{}, nil
}

func (fakeOAuth) Publish(_ context.Context, _ platform.Credentials, p platform.Payload, _ func(platform.RemoteRef) error) (platform.Result, error) {
	return platform.Result{Parts: []platform.RemoteRef{{ID: "1"}}}, nil
}

func (fakeOAuth) AuthorizeURL(app platform.App, redirectURI, state, challenge string) string {
	return "https://fake.test/authorize?" + url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "state": {state},
		"code_challenge": {challenge}}.Encode()
}

func (fakeOAuth) Exchange(_ context.Context, app platform.App, _, code, verifier string) ([]platform.Connection, error) {
	if app.ClientSecret != "s3cret" || verifier == "" {
		return nil, platform.Errorf(platform.Rejected, "bad app or verifier")
	}
	soon := time.Now().Add(time.Hour)
	acct := func(n string) platform.Connection {
		return platform.Connection{Account: platform.Account{ExternalID: "acct-" + n, Handle: "@" + n, DisplayName: "Account " + n},
			Credentials: platform.Credentials{"access_token": code + "-" + n, "page": n}, ExpiresAt: &soon}
	}
	switch code {
	case "many":
		return []platform.Connection{acct("a"), acct("b")}, nil
	default:
		return []platform.Connection{acct("a")}, nil
	}
}

func (fakeOAuth) Refresh(_ context.Context, _ platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["access_token"] == "revoked-a" {
		return nil, nil, platform.Errorf(platform.AuthRevoked, "token revoked")
	}
	later := time.Now().Add(60 * 24 * time.Hour)
	return platform.Credentials{"access_token": "renewed"}, &later, nil
}

func withFakeOAuth(_ *core.Config, adapters *[]platform.Adapter) {
	*adapters = append(*adapters, fakeOAuth{})
}

// signIn starts a sign-in and returns its state, as the platform would
// send it back.
func signIn(t *testing.T, w *world, live core.Actor, app *model.ProviderApp) string {
	t.Helper()
	target, err := w.s.BeginConnect(t.Context(), live, w.brand.ID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	if u.Query().Get("client_id") != "client-1" || u.Query().Get("code_challenge") == "" ||
		u.Query().Get("redirect_uri") != "https://araldo.test/connect/threads/callback" {
		t.Fatalf("authorize URL %s", target)
	}
	return u.Query().Get("state")
}

func TestOAuthConnections(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withFakeOAuth)
	ctx := t.Context()
	live := w.owner
	live.Livemode = true

	app, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: platform.Threads, ClientID: "client-1", ClientSecret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(app.ClientSecret, []byte("s3cret")) || app.Name != "Threads app" {
		t.Fatalf("app %+v: the secret must be encrypted", app)
	}
	if _, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: platform.Bluesky, ClientID: "x", ClientSecret: "y"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("an app for a platform without OAuth: %v", err)
	}
	if _, err := w.s.BeginConnect(ctx, w.owner, w.brand.ID, app.ID); kind(err) != apperr.KindInvalid {
		t.Fatalf("connecting in test mode: %v", err)
	}

	// One account: a channel at once, and the state is used up.
	state := signIn(t, w, live, app)
	res, err := w.s.FinishConnect(ctx, live, platform.Threads, state, "one")
	if err != nil {
		t.Fatal(err)
	}
	ch := res.Channels[0]
	if len(res.Channels) != 1 || ch.ExternalID != "acct-a" || ch.AppID == nil || *ch.AppID != app.ID || ch.TokenExpiresAt == nil || !ch.Livemode {
		t.Fatalf("channel %+v", ch)
	}
	if _, err := w.s.FinishConnect(ctx, live, platform.Threads, state, "one"); kind(err) != apperr.KindInvalid {
		t.Fatalf("a used state: %v", err)
	}

	// Several accounts: choose; the one already connected is reconnected,
	// not duplicated.
	state = signIn(t, w, live, app)
	res, err = w.s.FinishConnect(ctx, live, platform.Threads, state, "many")
	if err != nil || len(res.Choices) != 2 || len(res.Channels) != 0 {
		t.Fatalf("FinishConnect(many) = %+v, %v", res, err)
	}
	other := newWorld(t, withFakeOAuth)
	otherLive := other.owner
	otherLive.Livemode = true
	if _, err := other.s.ChooseConnections(ctx, otherLive, platform.Threads, res.State, []string{"acct-a"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("another org choosing from this sign-in: %v", err)
	}
	chosen, err := w.s.ChooseConnections(ctx, live, platform.Threads, res.State, []string{"acct-a", "acct-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen.Channels) != 2 {
		t.Fatalf("ChooseConnections connected %d channels, want 2", len(chosen.Channels))
	}
	all, err := w.s.Channels(ctx, live, &w.brand.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("%d live channels, want 2 (acct-a reconnected, acct-b added): %v", len(all), err)
	}

	// Tokens expiring soon are renewed; one the platform refuses needs
	// reconnecting.
	if n, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil || n != 2 {
		t.Fatalf("refresh: %d, %v", n, err)
	}
	// A refresh returns only the new tokens; what else the channel holds
	// (here its page) stays.
	renewed, err := w.s.Channel(ctx, live, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := core.StoredCredentials(w.s, renewed); err != nil || c["access_token"] != "renewed" || c["page"] != "a" {
		t.Fatalf("credentials after a refresh: %v, %v", c, err)
	}
	state = signIn(t, w, live, app)
	if _, err := w.s.FinishConnect(ctx, live, platform.Threads, state, "revoked"); err != nil {
		t.Fatal(err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(59 * 24 * time.Hour) } // the renewed tokens are due again too
	if _, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Channel(ctx, live, ch.ID)
	if err != nil || got.Status != model.ChannelNeedsReauth {
		t.Fatalf("a refused refresh: %+v, %v", got, err)
	}

	// Apps belong to their org. (Other tests' install apps are offered to
	// every org.)
	apps, err := other.s.ProviderApps(ctx, otherLive)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if !a.Install {
			t.Fatalf("another org sees %+v", a)
		}
	}
	if _, err := other.s.BeginConnect(ctx, otherLive, other.brand.ID, app.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("another org using this app: %v", err)
	}
	if err := w.s.DeleteProviderApp(ctx, live, app.ID); err != nil {
		t.Fatal(err)
	}
}

// TestInstallApps checks an app the operator registers for the install is
// offered to every org, signs in and renews for each, and only the
// operator manages it (ADR 0030).
func TestInstallApps(t *testing.T) {
	t.Parallel()
	w, other := newWorld(t, withFakeOAuth), newWorld(t, withFakeOAuth)
	ctx := t.Context()
	live, otherLive := w.owner, other.owner
	live.Livemode, otherLive.Livemode = true, true
	op := core.InstallOperator("araldo admin apps add")
	in := core.ProviderAppInput{Provider: platform.Threads, Name: "Threads " + uuid.NewString()[:8], ClientID: "client-1", ClientSecret: "s3cret"}

	if _, err := w.s.CreateInstallApp(ctx, live, in); kind(err) != apperr.KindForbidden {
		t.Fatalf("an org owner adding an install app: %v", err)
	}
	app, err := w.s.CreateInstallApp(ctx, op, in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(app.ClientSecret, []byte("s3cret")) || !app.Install {
		t.Fatalf("app %+v: the secret must be encrypted", app)
	}
	if _, err := w.s.CreateInstallApp(ctx, op, in); kind(err) != apperr.KindInvalid {
		t.Fatalf("a second app of the same name: %v", err)
	}
	var orgID *uuid.UUID
	var command string
	if err := open(t).Pool().QueryRow(ctx, `SELECT org_id, detail->>'operator_command' FROM audit_events WHERE action = 'install_app.create'
		AND target = $1`, id.Format(id.ProviderApp, app.ID)).Scan(&orgID, &command); err != nil || orgID != nil || command != "araldo admin apps add" {
		t.Fatalf("the audit: org %v, %q, %v", orgID, command, err)
	}

	// Both orgs are offered it, without its client ID, after their own.
	own, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: platform.Threads, ClientID: "client-1", ClientSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	apps, err := w.s.ProviderApps(ctx, live)
	if err != nil || len(apps) < 2 || apps[0].ID != own.ID {
		t.Fatalf("the org's apps first: %v, %v", apps, err)
	}
	for _, ow := range []*world{w, other} {
		a := ow.owner
		a.Livemode = true
		apps, err := ow.s.ProviderApps(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(apps, func(a *model.ProviderApp) bool { return a.ID == app.ID })
		if i < 0 || !apps[i].Install || apps[i].ClientID != "" || apps[i].ClientSecret != nil {
			t.Fatalf("the install app as an org sees it: %d in %v", i, apps)
		}
	}

	// Each org signs in through it, and its tokens renew.
	for _, ow := range []*world{w, other} {
		a := ow.owner
		a.Livemode = true
		res, err := ow.s.FinishConnect(ctx, a, platform.Threads, signIn(t, ow, a, app), "one")
		if err != nil {
			t.Fatal(err)
		}
		ch := res.Channels[0]
		if ch.AppID == nil || *ch.AppID != app.ID || !ch.InstallApp {
			t.Fatalf("channel %+v", ch)
		}
		if n, err := core.RefreshTokensOrg(ow.s, ow.org.ID); err != nil || n != 1 {
			t.Fatalf("refresh: %d, %v", n, err)
		}
		if got, err := ow.s.Channel(ctx, a, ch.ID); err != nil || got.AppID == nil || *got.AppID != app.ID || !got.InstallApp {
			t.Fatalf("channel read back: %+v, %v", got, err)
		}
	}

	// Orgs cannot change it; the operator can.
	if _, err := w.s.RenameProviderApp(ctx, live, app.ID, "mine"); kind(err) != apperr.KindNotFound {
		t.Fatalf("an org renaming an install app: %v", err)
	}
	if err := w.s.DeleteProviderApp(ctx, live, app.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("an org deleting an install app: %v", err)
	}
	if _, err := w.s.InstallApps(ctx, live); kind(err) != apperr.KindForbidden {
		t.Fatalf("an org listing install apps with their client IDs: %v", err)
	}
	renamed, err := w.s.RenameInstallApp(ctx, op, app.ID, in.Name+" renamed")
	if err != nil || renamed.Name != in.Name+" renamed" {
		t.Fatalf("rename: %+v, %v", renamed, err)
	}

	// Removed, it is no longer offered; channels made through it stay.
	if err := w.s.DeleteInstallApp(ctx, op, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.BeginConnect(ctx, live, w.brand.ID, app.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("connecting through a removed app: %v", err)
	}
	chs, err := other.s.Channels(ctx, otherLive, &other.brand.ID)
	if err != nil || len(chs) != 1 || chs[0].AppID != nil || chs[0].Status != model.ChannelActive {
		t.Fatalf("the channel after its app was removed: %v, %v", chs, err)
	}
	if err := w.s.DeleteInstallApp(ctx, op, app.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("removing it twice: %v", err)
	}
}
