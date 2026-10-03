// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestCleanTag(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"(none)": "", "(not set)": "", " (direct) ": "", "": "", "bluesky": "bluesky", "Post_01": "Post_01"} {
		if got := CleanTag(in); got != want {
			t.Errorf("CleanTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSandbox(t *testing.T) {
	t.Parallel()
	s := SandboxSource{}
	creds := platform.Credentials{"tags": "bluesky/social/release/post_01, bad"}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows, err := s.Report(t.Context(), creds, day, day.AddDate(0, 0, 1), []string{"Signup"})
	// 2 days × (untagged + 1 tag) × (traffic + 1 goal)
	if err != nil || len(rows) != 8 {
		t.Fatalf("Report = %d rows, %v", len(rows), err)
	}
	again, _ := s.Report(t.Context(), creds, day, day.AddDate(0, 0, 1), []string{"Signup"})
	for i := range rows {
		if rows[i] != again[i] {
			t.Fatalf("row %d changed: %+v then %+v", i, rows[i], again[i])
		}
	}
	untagged, tagged := rows[0], rows[2]
	if untagged.UTM != (UTM{}) || tagged.UTM.Content != "post_01" || untagged.Visitors <= tagged.Visitors {
		t.Fatalf("untagged %+v, tagged %+v: most traffic is untagged", untagged, tagged)
	}
	if goal := rows[1]; goal.Goal != "Signup" || goal.Visitors > untagged.Visitors {
		t.Fatalf("conversions %+v", goal)
	}
	if _, err := s.Report(t.Context(), platform.Credentials{"simulate": "auth_revoked"}, day, day, nil); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("simulated revocation: %v", err)
	}
}
