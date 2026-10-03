// SPDX-License-Identifier: AGPL-3.0-or-later

package ads

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// SandboxAds is the test-mode network. Its results are invented but
// stable: the same account, campaign and day always report the same
// numbers, so test mode has something to build reports against.
type SandboxAds struct{}

// sandboxCampaigns are every sandbox account's campaigns.
var sandboxCampaigns = []struct{ id, name string }{{"cmp_launch", "Launch"}, {"cmp_retarget", "Retargeting"}}

func (SandboxAds) Network() Network { return Sandbox }
func (SandboxAds) Name() string     { return "Sandbox" }

func (SandboxAds) Fields() []platform.Field {
	return []platform.Field{
		{Name: "name", Label: "Account name", Optional: true, Default: "Sandbox ads"},
		{Name: "currency", Label: "Currency", Optional: true, Default: "USD", Help: "ISO 4217, e.g. USD or EUR"},
		{Name: "simulate", Label: "Simulate", Optional: true,
			Help: "auth_revoked makes reading results fail as if the network revoked access"},
	}
}

func (SandboxAds) Verify(_ context.Context, _ platform.App, c platform.Credentials) (Account, error) {
	name := strings.TrimSpace(c["name"])
	if name == "" {
		name = "Sandbox ads"
	}
	currency := strings.ToUpper(strings.TrimSpace(c["currency"]))
	if currency == "" {
		currency = "USD"
	}
	if len(currency) != 3 {
		return Account{}, &platform.Error{Kind: platform.Rejected, Code: "currency_invalid", Msg: "currency is a three-letter ISO 4217 code"}
	}
	sum := sha256.Sum256([]byte(name + "/" + currency))
	return Account{ExternalID: "act_" + hex.EncodeToString(sum[:6]), Name: name, Currency: currency, Timezone: "UTC"}, nil
}

func (s SandboxAds) Report(ctx context.Context, app platform.App, c platform.Credentials, from, to time.Time) ([]Result, error) {
	if c["simulate"] == "auth_revoked" {
		return nil, platform.Errorf(platform.AuthRevoked, "the sandbox network revoked access (simulated)")
	}
	acct, err := s.Verify(ctx, app, c)
	if err != nil {
		return nil, err
	}
	var out []Result
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		for _, cmp := range sandboxCampaigns {
			sum := sha256.Sum256([]byte(acct.ExternalID + "/" + cmp.id + "/" + day.Format(time.DateOnly)))
			h := binary.BigEndian.Uint64(sum[:8])
			spend := int64(500 + h%5000)
			impressions := spend*2 + int64(h>>16%300)
			clicks := impressions/40 + int64(h>>32%7)
			out = append(out, Result{CampaignID: cmp.id, CampaignName: cmp.name, Day: day, Spend: spend,
				Impressions: impressions, Clicks: clicks, Results: clicks / 5})
		}
	}
	return out, nil
}
