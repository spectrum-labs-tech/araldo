// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// SandboxSource is the test-mode provider. Its numbers are invented but
// stable: every day has untagged traffic and traffic from the tags given
// in its "tags" field (comma-separated source/medium/campaign/content
// quadruples), each with conversions for every goal.
type SandboxSource struct{}

func (SandboxSource) Provider() Provider { return Sandbox }
func (SandboxSource) Name() string       { return "Sandbox" }

func (SandboxSource) Fields() []platform.Field {
	return []platform.Field{
		{Name: "site", Label: "Site", Optional: true, Default: "sandbox.example"},
		{Name: "tags", Label: "Tagged traffic", Optional: true,
			Help: "source/medium/campaign/content quadruples, comma-separated, e.g. bluesky/social/release/post_01…"},
		{Name: "simulate", Label: "Simulate", Optional: true, Help: "auth_revoked makes reading fail as if the key were revoked"},
	}
}

func (SandboxSource) Verify(_ context.Context, c platform.Credentials) (Site, error) {
	site := strings.TrimSpace(c["site"])
	if site == "" {
		site = "sandbox.example"
	}
	return Site{ID: site, Name: site, Timezone: "UTC"}, nil
}

func (s SandboxSource) Report(ctx context.Context, c platform.Credentials, from, to time.Time, goals []string) ([]Row, error) {
	if c["simulate"] == "auth_revoked" {
		return nil, platform.Errorf(platform.AuthRevoked, "the sandbox revoked the key (simulated)")
	}
	site, _ := s.Verify(ctx, c)
	tags := []UTM{{}}
	for _, q := range strings.Split(c["tags"], ",") {
		if parts := strings.Split(strings.TrimSpace(q), "/"); len(parts) == 4 {
			tags = append(tags, UTM{Source: parts[0], Medium: parts[1], Campaign: parts[2], Content: parts[3]})
		}
	}
	var out []Row
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		for _, t := range tags {
			h := hash(site.ID, day.Format(time.DateOnly), t.Source, t.Medium, t.Campaign, t.Content)
			visitors := int64(20 + h%180)
			if t == (UTM{}) {
				visitors *= 5 // most traffic comes untagged
			}
			out = append(out, Row{Day: day, UTM: t, Visitors: visitors, Visits: visitors + int64(h>>8%20)})
			for i, g := range goals {
				conv := visitors / int64(8+i*4)
				out = append(out, Row{Day: day, UTM: t, Goal: g, Visitors: conv, Events: conv + int64(h>>16%3)})
			}
		}
	}
	return out, nil
}

func hash(parts ...string) uint64 {
	sum := sha256.Sum256([]byte(strings.Join(parts, "/")))
	return binary.BigEndian.Uint64(sum[:8])
}
