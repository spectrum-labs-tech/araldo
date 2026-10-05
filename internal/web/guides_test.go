// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/ads/reddit"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/bluesky"
	"github.com/spectrum-labs-tech/araldo/internal/platform/discord"
	"github.com/spectrum-labs-tech/araldo/internal/platform/facebook"
	"github.com/spectrum-labs-tech/araldo/internal/platform/gab"
	"github.com/spectrum-labs-tech/araldo/internal/platform/instagram"
	"github.com/spectrum-labs-tech/araldo/internal/platform/linkedin"
	"github.com/spectrum-labs-tech/araldo/internal/platform/mastodon"
	"github.com/spectrum-labs-tech/araldo/internal/platform/pinterest"
	"github.com/spectrum-labs-tech/araldo/internal/platform/telegram"
	"github.com/spectrum-labs-tech/araldo/internal/platform/threads"
	"github.com/spectrum-labs-tech/araldo/internal/platform/tiktok"
	"github.com/spectrum-labs-tech/araldo/internal/platform/x"
	"github.com/spectrum-labs-tech/araldo/internal/platform/youtube"
)

// TestAppGuides checks that every platform that connects through a
// developer app has a setup guide in the dashboard, a site to register at,
// and a section in docs/operations.md naming its redirect URI, and that no
// guide is left for a platform that no longer needs one.
func TestAppGuides(t *testing.T) {
	t.Parallel()
	c := http.DefaultClient
	needApps := map[platform.Provider]bool{}
	for _, a := range []platform.Adapter{
		bluesky.New(c), mastodon.New(c), gab.New(c), discord.New(c), telegram.New(c), x.New(c), linkedin.New(c),
		pinterest.New(c), youtube.New(c), tiktok.New(c), threads.New(c), facebook.New(c), instagram.New(c),
	} {
		if _, ok := a.(platform.Connector); ok {
			needApps[a.Provider()] = true
		}
	}
	needApps[ads.Provider(reddit.New(c).Network())] = true
	if len(needApps) < 9 {
		t.Fatalf("only %d platforms connect through a developer app; has platform.Connector changed?", len(needApps))
	}
	docs, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	for p := range needApps {
		g, ok := appGuides[p]
		switch {
		case !ok:
			t.Errorf("%s connects through a developer app but has no guide in appGuides", p)
		case len(g.Steps) == 0 || g.ClientID == "" || g.Confidential == "":
			t.Errorf("%s's guide lacks steps, or what goes in Client ID or Client secret", p)
		}
		if developerSites[p] == "" {
			t.Errorf("%s has no developer site", p)
		}
		if !strings.Contains(string(docs), "connect/"+string(p)+"/callback") {
			t.Errorf("docs/operations.md does not give %s's redirect URI", p)
		}
	}
	for p := range appGuides {
		if !needApps[p] {
			t.Errorf("appGuides has a guide for %s, which connects without a developer app", p)
		}
	}
}
