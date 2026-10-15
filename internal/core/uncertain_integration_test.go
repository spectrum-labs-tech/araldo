// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// forgetfulPlatform stands in for Mastodon: idempotent, but it remembers
// a key for an hour only, and every publish is uncertain.
type forgetfulPlatform struct{}

func (forgetfulPlatform) Provider() platform.Provider { return platform.Mastodon }
func (forgetfulPlatform) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Mastodon)
	return r
}
func (forgetfulPlatform) Fields() []platform.Field {
	return []platform.Field{{Name: "token", Label: "Token", Secret: true}}
}
func (forgetfulPlatform) Idempotent() bool                 { return true }
func (forgetfulPlatform) IdempotencyWindow() time.Duration { return time.Hour }
func (forgetfulPlatform) Verify(context.Context, platform.Credentials) (platform.Account, error) {
	return platform.Account{ExternalID: "forgetful", Handle: "@forgetful", DisplayName: "Forgetful"}, nil
}
func (forgetfulPlatform) Publish(context.Context, platform.Credentials, platform.Payload, func(platform.RemoteRef) error) (platform.Result, error) {
	return platform.Result{}, platform.Errorf(platform.Uncertain, "the connection dropped after sending")
}

// TestUncertainRetriesStayInTheWindow checks an uncertain attempt on a
// platform that forgets keys is retried while it remembers the first one,
// and needs a person once it may have forgotten it (ADR 0011).
func TestUncertainRetriesStayInTheWindow(t *testing.T) {
	t.Parallel()
	w := newWorld(t, func(_ *core.Config, adapters *[]platform.Adapter) { *adapters = append(*adapters, forgetfulPlatform{}) })
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	ch, err := w.s.ConnectChannel(ctx, live, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Mastodon,
		Fields: map[string]string{"token": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	// Due days from now, by this test's clock only, so no other test's
	// publisher, whose platforms do not include this one, takes it.
	start := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
	now := start
	w.s.Now = func() time.Time { return now }
	by := start.Add(48 * time.Hour)
	p, err := w.s.CreatePost(ctx, live, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Maybe posted"},
		Channels: []uuid.UUID{ch.ID}, PublishAt: start.Format(time.RFC3339), PublishBy: &by})
	if err != nil {
		t.Fatal(err)
	}
	target := func() model.Target {
		t.Helper()
		got, err := w.s.Post(ctx, live, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Targets[0]
	}
	if _, err := core.PublishDueOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	if got := target(); got.Status != model.TargetQueued || got.Attempts != 1 {
		t.Fatalf("within the window: %s after %d attempts, want queued to retry", got.Status, got.Attempts)
	}
	now = start.Add(2 * time.Hour) // the platform may have forgotten the key
	if _, err := core.PublishDueOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}
	if got := target(); got.Status != model.TargetNeedsAttention || got.Attempts != 2 {
		t.Fatalf("past the window: %s after %d attempts, want needs_attention", got.Status, got.Attempts)
	}
}
