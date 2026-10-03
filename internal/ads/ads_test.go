// SPDX-License-Identifier: AGPL-3.0-or-later

package ads

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestSandboxIsStable(t *testing.T) {
	t.Parallel()
	s := SandboxAds{}
	creds := platform.Credentials{"name": "Otium", "currency": "usd"}
	acct, err := s.Verify(t.Context(), platform.App{}, creds)
	if err != nil || acct.Currency != "USD" || acct.Name != "Otium" || acct.ExternalID == "" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	first, err := s.Report(t.Context(), platform.App{}, creds, day, day.AddDate(0, 0, 2))
	if err != nil || len(first) != 3*len(sandboxCampaigns) {
		t.Fatalf("Report = %d results, %v", len(first), err)
	}
	again, _ := s.Report(t.Context(), platform.App{}, creds, day, day.AddDate(0, 0, 2))
	for i := range first {
		r := first[i]
		if r != again[i] {
			t.Fatalf("result %d changed: %+v then %+v", i, r, again[i])
		}
		if r.Spend <= 0 || r.Clicks <= 0 || r.Impressions < r.Clicks || r.Results > r.Clicks {
			t.Fatalf("implausible result %+v", r)
		}
	}
	other, _ := s.Report(t.Context(), platform.App{}, platform.Credentials{"name": "VCDS"}, day, day)
	if other[0].Spend == first[0].Spend && other[1].Spend == first[1].Spend {
		t.Fatal("different accounts should report different numbers")
	}
}

func TestSandboxFailures(t *testing.T) {
	t.Parallel()
	s := SandboxAds{}
	if _, err := s.Verify(t.Context(), platform.App{}, platform.Credentials{"currency": "dollars"}); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a bad currency: %v", err)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Report(t.Context(), platform.App{}, platform.Credentials{"simulate": "auth_revoked"}, day, day); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("simulated revocation: %v", err)
	}
}

func TestDate(t *testing.T) {
	t.Parallel()
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 03:00 UTC on Oct 2 is still Oct 1 in Denver.
	got := Date(time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC), denver)
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Date = %s, want %s", got, want)
	}
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	r := NewRegistry(SandboxAds{})
	if _, ok := r.Get(Sandbox); !ok || len(r.Networks()) != 1 {
		t.Fatalf("registry %v", r.Networks())
	}
	if _, ok := r.Get("reddit"); ok {
		t.Fatal("an unregistered network")
	}
}
