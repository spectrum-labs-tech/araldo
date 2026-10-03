// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

func connectSandboxAds(t *testing.T, w *world, a core.Actor, name string, extra map[string]string) *model.AdAccount {
	t.Helper()
	fields := map[string]string{"name": name}
	for k, v := range extra {
		fields[k] = v
	}
	ac, err := w.s.ConnectAdAccount(t.Context(), a, core.AdAccountInput{BrandID: w.brand.ID, Network: ads.Sandbox, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	return ac
}

func TestAdAccountsAreReadAndSummarized(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	ac := connectSandboxAds(t, w, w.owner, "Otium", nil)
	if ac.Currency != "USD" || ac.Status != model.AdAccountActive || ac.ReadAt != nil {
		t.Fatalf("connected %+v", ac)
	}
	if _, err := w.s.ConnectAdAccount(ctx, w.owner, core.AdAccountInput{BrandID: w.brand.ID, Network: ads.Sandbox,
		Fields: map[string]string{"name": "Otium"}}); kind(err) != apperr.KindConflict {
		t.Fatalf("connecting the same account twice: %v", err)
	}

	// The first read backfills 30 days of both campaigns; the next is a day
	// later. Readers in other tests may read it first, which is fine.
	var got *model.AdAccount
	waitFor(t, "the account to be read", func() {
		if _, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		var err error
		got, err = w.s.AdAccount(ctx, w.owner, ac.ID)
		return err == nil && got.ReadAt != nil
	})
	if got.NextReadAt.Sub(*got.ReadAt) != core.AdsReadEvery {
		t.Fatalf("after reading: %+v", got)
	}
	if n, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil || n != 0 {
		t.Fatalf("reading again before it is due: %d, %v", n, err)
	}

	sum := func(group store.AdsGroup, days int) *core.AdsSummary {
		t.Helper()
		until := ads.Date(time.Now(), time.UTC)
		s, err := w.s.AdsSummary(ctx, w.owner, core.AdsFilter{GroupBy: group, Since: until.AddDate(0, 0, -(days - 1)), Until: until, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	byCampaign := sum(store.AdsByCampaign, 30)
	if len(byCampaign.Rows) != 2 || byCampaign.Rows[0].Spend < byCampaign.Rows[1].Spend || byCampaign.Rows[0].Currency != "USD" ||
		byCampaign.Rows[0].AccountID == nil || *byCampaign.Rows[0].AccountID != ac.ID {
		t.Fatalf("by campaign: %+v", byCampaign.Rows)
	}
	byDay := sum(store.AdsByDay, 30)
	if len(byDay.Rows) != core.AdsBackfillDays || byDay.Rows[0].Key > byDay.Rows[1].Key {
		t.Fatalf("by day: %d rows, want %d, oldest first", len(byDay.Rows), core.AdsBackfillDays)
	}
	var total int64
	for _, r := range byDay.Rows {
		total += r.Spend
	}
	byAccount := sum(store.AdsByAccount, 30)
	if len(byAccount.Rows) != 1 || byAccount.Rows[0].Spend != total || byAccount.Rows[0].Label != "Otium" {
		t.Fatalf("by account %+v, want spend %d", byAccount.Rows, total)
	}
	if week := sum(store.AdsByAccount, 7); week.Rows[0].Spend >= total {
		t.Fatalf("a week (%d) should be less than 30 days (%d)", week.Rows[0].Spend, total)
	}
	if _, err := w.s.AdsSummary(ctx, w.owner, core.AdsFilter{GroupBy: "hour"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("an unknown grouping: %v", err)
	}

	// Disconnecting forgets the results.
	if err := w.s.DeleteAdAccount(ctx, w.owner, ac.ID); err != nil {
		t.Fatal(err)
	}
	if rows := sum(store.AdsByAccount, 30).Rows; len(rows) != 0 {
		t.Fatalf("results after disconnecting: %+v", rows)
	}
}

func TestAdAccountRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()

	// Sandbox only in test mode; the live mode has no sandbox.
	live := w.owner
	live.Livemode = true
	if _, err := w.s.ConnectAdAccount(ctx, live, core.AdAccountInput{BrandID: w.brand.ID, Network: ads.Sandbox}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a sandbox account in live mode: %v", err)
	}
	if _, err := w.s.ConnectAdAccount(ctx, w.owner, core.AdAccountInput{BrandID: w.brand.ID, Network: "reddit"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a real network in test mode: %v", err)
	}
	if nets := w.s.AdNetworks(false); len(nets) != 1 || nets[0].Network != ads.Sandbox {
		t.Fatalf("test-mode networks %+v", nets)
	}
	if nets := w.s.AdNetworks(true); len(nets) != 0 {
		t.Fatalf("live networks on an install with none: %+v", nets)
	}

	// ads:write is explicit-only: a full-access key reads but cannot connect.
	plain, _, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "full"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.s.AuthenticateKey(ctx, plain, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ConnectAdAccount(ctx, key, core.AdAccountInput{BrandID: w.brand.ID, Network: ads.Sandbox}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a full-access key connecting: %v", err)
	}
	if _, err := w.s.AdAccounts(ctx, key, nil); err != nil {
		t.Fatalf("a full-access key reading: %v", err)
	}
	adsKey := key
	adsKey.Scopes = []string{"ads:write"}
	if _, err := w.s.ConnectAdAccount(ctx, adsKey, core.AdAccountInput{BrandID: w.brand.ID, Network: ads.Sandbox,
		Fields: map[string]string{"name": "By key"}}); err != nil {
		t.Fatalf("an ads:write key connecting: %v", err)
	}

	// A network that revokes access marks the account; nothing else stops.
	revoked := connectSandboxAds(t, w, w.owner, "Revoked", map[string]string{"simulate": "auth_revoked"})
	var accts []*model.AdAccount
	waitFor(t, "the revoked account flagged and the healthy one read", func() {
		if _, err := core.CollectAdsOrg(w.s, w.org.ID); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		var err error
		if accts, err = w.s.AdAccounts(ctx, w.owner, nil); err != nil || len(accts) != 2 {
			return false
		}
		for _, a := range accts {
			if a.ID == revoked.ID && a.Status != model.AdAccountNeedsReauth || a.ID != revoked.ID && a.ReadAt == nil {
				return false
			}
		}
		return true
	})
	for _, a := range accts {
		if a.ID == revoked.ID && a.StatusNote == "" {
			t.Fatalf("a revoked account says nothing: %+v", a)
		}
	}
}

// waitFor runs step until done reports true, or fails after 15 seconds.
// Readers in other tests claim due work in every org, so a test waits for
// its own rows rather than counting what its own step did.
func waitFor(t *testing.T, what string, step func(), done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		step()
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
