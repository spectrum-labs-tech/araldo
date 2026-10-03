// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/bluesky"
	"github.com/spectrum-labs-tech/araldo/internal/platform/x"
)

// X connects with a sign-in through the org's app, or with keys pasted by
// hand; a platform without sign-in offers only the keys.
func TestConnectOffersSignInAndKeys(t *testing.T) {
	t.Parallel()
	d := newDash(t, x.New(http.DefaultClient), bluesky.New(http.DefaultClient))
	ctx := t.Context()
	if err := d.s.SwitchContext(ctx, d.login.Session, d.owner.OrgID, true); err != nil {
		t.Fatal(err)
	}
	live := d.owner
	live.Livemode = true
	page := func(provider string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, "/channels/new?provider="+provider, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", provider, rec.Code)
		}
		return rec.Body.String()
	}

	got := page("x")
	if !strings.Contains(got, "Add your X app") || !strings.Contains(got, "Or enter credentials by hand") || !strings.Contains(got, `name="field_api_key"`) {
		t.Fatalf("X without an app should point to adding one and offer pasted keys:\n%s", got)
	}
	if _, err := d.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: platform.X, ClientID: "client", ClientSecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	got = page("x")
	if !strings.Contains(got, `action="/connect/x/start"`) || !strings.Contains(got, "Sign in with X") || !strings.Contains(got, `name="field_api_key"`) {
		t.Fatalf("X with an app should offer the sign-in and pasted keys:\n%s", got)
	}
	if got := page("bluesky"); strings.Contains(got, "Sign in with") || strings.Contains(got, "Or enter credentials by hand") || !strings.Contains(got, `name="field_app_password"`) {
		t.Fatalf("Bluesky has only pasted credentials:\n%s", got)
	}
}
