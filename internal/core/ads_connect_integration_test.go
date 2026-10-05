// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeAdsOAuth is an ad network whose accounts connect with a sign-in, as
// Reddit's do. Reading needs the app's secret, as renewing a token would.
type fakeAdsOAuth struct{}

func (fakeAdsOAuth) Network() ads.Network     { return "fake" }
func (fakeAdsOAuth) Name() string             { return "Fake" }
func (fakeAdsOAuth) Fields() []platform.Field { return nil }
func (fakeAdsOAuth) AuthorizeURL(app platform.App, _, state, _ string) string {
	return "https://fake.test/authorize?" + url.Values{"client_id": {app.ClientID}, "state": {state}}.Encode()
}

func (fakeAdsOAuth) Exchange(_ context.Context, _ platform.App, _, _, _ string) ([]platform.Connection, error) {
	acct := func(n string) platform.Connection {
		return platform.Connection{Account: platform.Account{ExternalID: "act_" + n, DisplayName: "Account " + n},
			Credentials: platform.Credentials{"refresh_token": "r", "account_id": "act_" + n}}
	}
	return []platform.Connection{acct("a"), acct("b")}, nil
}

func (fakeAdsOAuth) Verify(_ context.Context, app platform.App, c platform.Credentials) (ads.Account, error) {
	if app.ClientSecret != "s3cret" {
		return ads.Account{}, platform.Errorf(platform.AuthRevoked, "no app")
	}
	return ads.Account{ExternalID: c["account_id"], Name: "Account " + c["account_id"], Currency: "USD", Timezone: "UTC"}, nil
}

func (f fakeAdsOAuth) Report(ctx context.Context, app platform.App, c platform.Credentials, from, _ time.Time) ([]ads.Result, error) {
	if _, err := f.Verify(ctx, app, c); err != nil {
		return nil, err
	}
	return []ads.Result{{CampaignID: "c1", CampaignName: "Launch", Day: from, Spend: 100, Clicks: 4, Impressions: 90}}, nil
}

func TestAdNetworkSignIn(t *testing.T) {
	t.Parallel()
	w := newWorld(t, func(cfg *core.Config, _ *[]platform.Adapter) { cfg.AdNetworks = []ads.Reporter{fakeAdsOAuth{}} })
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	provider := ads.Provider("fake")
	if nets := w.s.AdNetworks(true); len(nets) != 1 || !nets[0].OAuth || nets[0].Provider != provider {
		t.Fatalf("live networks %+v", nets)
	}
	app, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: provider, ClientID: "client-1", ClientSecret: "s3cret"})
	if err != nil || app.Name != "Fake Ads app" {
		t.Fatalf("app %+v, %v", app, err)
	}

	signIn := func() string {
		t.Helper()
		target, err := w.s.BeginConnect(ctx, live, w.brand.ID, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(target)
		return u.Query().Get("state")
	}
	res, err := w.s.FinishConnect(ctx, live, provider, signIn(), "code")
	if err != nil || len(res.Choices) != 2 {
		t.Fatalf("FinishConnect = %+v, %v", res, err)
	}
	res, err = w.s.ChooseConnections(ctx, live, provider, res.State, []string{"act_a", "act_b"})
	if err != nil || len(res.AdAccounts) != 2 || len(res.Channels) != 0 {
		t.Fatalf("ChooseConnections = %+v, %v", res, err)
	}
	first := res.AdAccounts[0]
	if first.AppID == nil || *first.AppID != app.ID || first.Network != "fake" || first.Currency != "USD" {
		t.Fatalf("ad account %+v", first)
	}

	// Reading uses the app's credentials. The accounts are due by the
	// database's clock, which may run a little ahead of this one, so wait.
	var got *model.AdAccount
	waitFor(t, "the accounts to be read", func() {
		if _, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		got, err = w.s.AdAccount(ctx, live, first.ID)
		return err == nil && got.ReadAt != nil
	})
	if got.Status != model.AdAccountActive {
		t.Fatalf("after reading: %+v", got)
	}

	// Signing in again reconnects; it never duplicates.
	res, err = w.s.FinishConnect(ctx, live, provider, signIn(), "code")
	if err != nil {
		t.Fatal(err)
	}
	res, err = w.s.ChooseConnections(ctx, live, provider, res.State, []string{"act_a"})
	if err != nil || len(res.AdAccounts) != 1 || res.AdAccounts[0].ID != first.ID {
		t.Fatalf("reconnecting: %+v, %v", res, err)
	}
	if accts, err := w.s.AdAccounts(ctx, live, nil); err != nil || len(accts) != 2 {
		t.Fatalf("%d accounts after reconnecting, want 2: %v", len(accts), err)
	}
}
