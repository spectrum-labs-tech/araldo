// SPDX-License-Identifier: AGPL-3.0-or-later

package email

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// SandboxMailer is the test-mode provider. It sends nothing, keeps nothing
// and invents stable numbers, so any process can answer for a campaign
// another created: a campaign's ID is derived from its tag, and a
// campaign asked about has been sent.
type SandboxMailer struct{}

// SandboxAudiences are every sandbox account's audiences.
var SandboxAudiences = []Audience{
	{ID: "list-1", Name: "Newsletter", Kind: "list", Size: 1200},
	{ID: "list-2", Name: "Customers", Kind: "list", Size: 340},
	{ID: "segment-1", Name: "Opened in the last 90 days", Kind: "segment", Size: -1},
}

func (SandboxMailer) Provider() Provider { return Sandbox }
func (SandboxMailer) Name() string       { return "Sandbox" }
func (SandboxMailer) Unsubscribe() string {
	return "https://sandbox.example/unsubscribe"
}

func (SandboxMailer) Fields() []platform.Field {
	return []platform.Field{
		{Name: "simulate", Label: "Simulate", Optional: true,
			Help: "auth_revoked fails as if the key were revoked; uncertain loses the answer to a hand-off; stopped reports campaigns as stopped in the provider"},
	}
}

func (SandboxMailer) Verify(_ context.Context, c platform.Credentials, from Address) (Account, error) {
	if c["simulate"] == "auth_revoked" {
		return Account{}, platform.Errorf(platform.AuthRevoked, "the sandbox revoked the key (simulated)")
	}
	return Account{ExternalID: "sandbox", Name: "Sandbox"}, nil
}

func (SandboxMailer) Audiences(_ context.Context, c platform.Credentials) ([]Audience, error) {
	if c["simulate"] == "auth_revoked" {
		return nil, platform.Errorf(platform.AuthRevoked, "the sandbox revoked the key (simulated)")
	}
	return append([]Audience(nil), SandboxAudiences...), nil
}

func sandboxCampaign(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return "sbx_" + hex.EncodeToString(sum[:8])
}

func (SandboxMailer) Schedule(_ context.Context, c platform.Credentials, m Message, audiences []string, _ time.Time) (string, error) {
	switch c["simulate"] {
	case "auth_revoked":
		return "", platform.Errorf(platform.AuthRevoked, "the sandbox revoked the key (simulated)")
	case "uncertain":
		return "", platform.Errorf(platform.Uncertain, "the sandbox lost the answer (simulated); the campaign exists")
	}
	if len(audiences) == 0 {
		return "", platform.Errorf(platform.Rejected, "a campaign needs an audience")
	}
	return sandboxCampaign(m.Tag), nil
}

func (SandboxMailer) Find(_ context.Context, c platform.Credentials, tag string) (string, error) {
	if c["simulate"] == "uncertain" {
		return sandboxCampaign(tag), nil
	}
	return "", nil
}

func (SandboxMailer) Reschedule(context.Context, platform.Credentials, string, time.Time) error {
	return nil
}
func (SandboxMailer) Cancel(context.Context, platform.Credentials, string) error { return nil }

func (SandboxMailer) Campaign(_ context.Context, c platform.Credentials, campaignID string) (Campaign, error) {
	if c["simulate"] == "stopped" {
		return Campaign{ID: campaignID, Status: CampaignStopped}, nil
	}
	sum := sha256.Sum256([]byte(campaignID))
	h := binary.BigEndian.Uint64(sum[:8])
	n := int64(800 + h%800)
	delivered := n - n/50
	return Campaign{ID: campaignID, Status: CampaignSent, Results: Results{Recipients: n, Delivered: delivered,
		Opens: delivered * 2 / 5, Clicks: delivered / 20, Unsubscribes: int64(1 + h>>8%5), Bounces: n - delivered,
		Complaints: int64(h >> 16 % 2)}}, nil
}

func (SandboxMailer) SendTest(_ context.Context, c platform.Credentials, _ Message, to []string) error {
	for _, addr := range to {
		if !strings.Contains(addr, "@") {
			return platform.Errorf(platform.Rejected, "%q is not an email address", addr)
		}
	}
	return nil
}
