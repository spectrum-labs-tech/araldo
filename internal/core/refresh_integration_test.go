// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// rotatingOAuth is a platform whose tokens last ttl and whose refresh
// tokens are single-use, as X's are: each renewal replaces the refresh
// token, and the spent one is refused. Without a refresh token (renewable
// false) it renews nothing, as most LinkedIn apps.
type rotatingOAuth struct {
	provider  platform.Provider
	ttl       time.Duration
	renewable bool

	mu      sync.Mutex
	current string // the one refresh token that works
	renewed int
	refused int
}

func (f *rotatingOAuth) Provider() platform.Provider { return f.provider }
func (f *rotatingOAuth) Rules() platform.Rules {
	r, _ := platform.RulesFor(f.provider)
	return r
}
func (f *rotatingOAuth) Fields() []platform.Field { return nil }
func (f *rotatingOAuth) Idempotent() bool         { return false }
func (f *rotatingOAuth) Verify(context.Context, platform.Credentials) (platform.Account, error) {
	return platform.Account{}, nil
}

func (f *rotatingOAuth) Publish(context.Context, platform.Credentials, platform.Payload, func(platform.RemoteRef) error) (platform.Result, error) {
	return platform.Result{}, platform.Errorf(platform.Rejected, "not used")
}

func (f *rotatingOAuth) AuthorizeURL(app platform.App, redirectURI, state, challenge string) string {
	return "https://fake.test/authorize?state=" + state
}

func (f *rotatingOAuth) Exchange(_ context.Context, _ platform.App, _, _, _ string) ([]platform.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	creds := platform.Credentials{"access_token": "at-0"}
	if f.renewable {
		f.current = "rt-0"
		creds["refresh_token"] = f.current
	}
	exp := time.Now().Add(f.ttl)
	return []platform.Connection{{Account: platform.Account{ExternalID: "acct", Handle: "@acct", DisplayName: "Account"},
		Credentials: creds, ExpiresAt: &exp}}, nil
}

func (f *rotatingOAuth) Refresh(_ context.Context, _ platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if !f.renewable {
		return nil, nil, platform.ErrNoRefresh
	}
	time.Sleep(20 * time.Millisecond) // a network round trip, so racing renewals overlap
	f.mu.Lock()
	defer f.mu.Unlock()
	if c["refresh_token"] != f.current {
		f.refused++
		return nil, nil, platform.Errorf(platform.AuthRevoked, "refresh token %s was already used", c["refresh_token"])
	}
	f.renewed++
	f.current = fmt.Sprintf("rt-%d", f.renewed)
	exp := time.Now().Add(f.ttl)
	return platform.Credentials{"access_token": fmt.Sprintf("at-%d", f.renewed), "refresh_token": f.current}, &exp, nil
}

// connectRotating connects one live channel through f and returns it.
func connectRotating(t *testing.T, f *rotatingOAuth) (*world, core.Actor, *model.Channel) {
	t.Helper()
	w := newWorld(t, func(_ *core.Config, adapters *[]platform.Adapter) { *adapters = append(*adapters, f) })
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	app, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: f.provider, ClientID: "client-1", ClientSecret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := w.s.BeginConnect(ctx, live, w.brand.ID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := target[strings.Index(target, "state=")+len("state="):]
	res, err := w.s.FinishConnect(ctx, live, f.provider, state, "code")
	if err != nil || len(res.Channels) != 1 {
		t.Fatalf("FinishConnect = %+v, %v", res, err)
	}
	return w, live, res.Channels[0]
}

// Renewals that race never spend the same single-use refresh token twice,
// which the platform would answer by revoking the channel.
func TestRacingRenewalsKeepTheChannel(t *testing.T) {
	t.Parallel()
	f := &rotatingOAuth{provider: platform.X, ttl: 2 * time.Hour, renewable: true}
	w, live, ch := connectRotating(t, f)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := w.s.Channel(t.Context(), live, ch.ID)
	if err != nil || got.Status != model.ChannelActive || f.refused != 0 || f.renewed == 0 {
		t.Fatalf("after racing renewals: %+v, %v; renewed %d, refused %d", got, err, f.renewed, f.refused)
	}
	// The stored refresh token is the live one: another renewal works.
	if _, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil || f.refused != 0 {
		t.Fatalf("renewing again: %v, refused %d", err, f.refused)
	}
}

// A token about to expire is renewed before it is used, in case the hourly
// renewal fell behind.
func TestTokensAreRenewedBeforeUse(t *testing.T) {
	t.Parallel()
	f := &rotatingOAuth{provider: platform.X, ttl: 2 * time.Minute, renewable: true}
	w, live, ch := connectRotating(t, f)
	creds, err := core.UsableCredentials(w.s, ch)
	if err != nil || creds["access_token"] != "at-1" || f.renewed != 1 {
		t.Fatalf("credentials %v, %v; renewed %d", creds, err, f.renewed)
	}
	// A fresh token is used as it is.
	fresh, err := w.s.Channel(t.Context(), live, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.ttl = time.Hour
	w.s.Now = func() time.Time { return time.Now().Add(-time.Hour) }
	if creds, err := core.UsableCredentials(w.s, fresh); err != nil || creds["access_token"] != "at-1" || f.renewed != 1 {
		t.Fatalf("a fresh token: %v, %v; renewed %d", creds, err, f.renewed)
	}
}

// A token that cannot be renewed keeps working until it expires; the
// channel says when to sign in again, then needs it.
func TestUnrenewableTokens(t *testing.T) {
	t.Parallel()
	f := &rotatingOAuth{provider: platform.LinkedIn, ttl: 3 * 24 * time.Hour}
	w, live, ch := connectRotating(t, f)
	if _, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Channel(t.Context(), live, ch.ID)
	if err != nil || got.Status != model.ChannelActive || !strings.HasPrefix(got.StatusNote, "Sign in again before ") {
		t.Fatalf("before expiry: %+v, %v", got, err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(4 * 24 * time.Hour) }
	if _, err := core.RefreshTokensOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := w.s.Channel(t.Context(), live, ch.ID); err != nil || got.Status != model.ChannelNeedsReauth {
		t.Fatalf("after expiry: %+v, %v", got, err)
	}
}
