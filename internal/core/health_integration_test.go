// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// checkedPlatform stands in for Bluesky: each account's Verify works,
// fails as revoked, or fails otherwise, as the test sets it.
type checkedPlatform struct {
	mu      sync.Mutex
	revoked map[string]bool
	broken  map[string]bool
}

func (*checkedPlatform) Provider() platform.Provider { return platform.Bluesky }
func (*checkedPlatform) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Bluesky)
	return r
}
func (*checkedPlatform) Fields() []platform.Field {
	return []platform.Field{{Name: "identifier", Label: "Handle"}, {Name: "app_password", Label: "App password", Secret: true}}
}
func (*checkedPlatform) Idempotent() bool { return false }
func (f *checkedPlatform) Verify(_ context.Context, c platform.Credentials) (platform.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch who := c["identifier"]; {
	case f.revoked[who]:
		return platform.Account{}, platform.Errorf(platform.AuthRevoked, "app password revoked")
	case f.broken[who]:
		return platform.Account{}, platform.Errorf(platform.Transient, "HTTP 503")
	default:
		return platform.Account{ExternalID: who, Handle: "@" + who, DisplayName: who}, nil
	}
}
func (*checkedPlatform) Publish(context.Context, platform.Credentials, platform.Payload, func(platform.RemoteRef) error) (platform.Result, error) {
	return platform.Result{}, platform.Errorf(platform.Rejected, "not used")
}

func TestDailyChannelChecks(t *testing.T) {
	t.Parallel()
	f := &checkedPlatform{revoked: map[string]bool{}, broken: map[string]bool{}}
	w := newWorld(t, func(_ *core.Config, adapters *[]platform.Adapter) { *adapters = append(*adapters, f) })
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	chs := map[string]*model.Channel{}
	for _, who := range []string{"fine", "revoked", "broken"} {
		ch, err := w.s.ConnectChannel(ctx, live, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Bluesky,
			Fields: map[string]string{"identifier": who, "app_password": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		chs[who] = ch
	}
	f.mu.Lock()
	f.revoked["revoked"], f.broken["broken"] = true, true
	f.mu.Unlock()

	// The live channels are checked; the test-mode sandbox channel is not.
	if n, err := core.CheckChannelsOrg(w.s, w.org.ID); err != nil || n != 3 {
		t.Fatalf("check: %d, %v", n, err)
	}
	get := func(who string) *model.Channel {
		t.Helper()
		ch, err := w.s.Channel(ctx, live, chs[who].ID)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	if ch := get("fine"); ch.CheckedAt == nil || ch.CheckError != "" || ch.Status != model.ChannelActive {
		t.Fatalf("a working channel: %+v", ch)
	}
	if ch := get("revoked"); ch.Status != model.ChannelNeedsReauth || ch.CheckError == "" {
		t.Fatalf("revoked credentials should need reconnecting: %+v", ch)
	}
	if ch := get("broken"); ch.Status != model.ChannelActive || !strings.Contains(ch.CheckError, "503") {
		t.Fatalf("a platform outage is noted, not a reconnect: %+v", ch)
	}
	// Checked once a day.
	if n, err := core.CheckChannelsOrg(w.s, w.org.ID); err != nil || n != 0 {
		t.Fatalf("checking again at once: %d, %v", n, err)
	}
}
