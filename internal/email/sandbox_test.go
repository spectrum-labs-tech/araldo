// SPDX-License-Identifier: AGPL-3.0-or-later

package email

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestSandboxIsStableAcrossProcesses(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var s SandboxMailer
	msg := Message{Tag: "nldel_1"}
	id, err := s.Schedule(ctx, platform.Credentials{}, msg, []string{"list-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Another process answers for the same campaign the same way.
	a, _ := SandboxMailer{}.Campaign(ctx, platform.Credentials{}, id)
	b, _ := SandboxMailer{}.Campaign(ctx, platform.Credentials{}, id)
	if a.Status != CampaignSent || a.Results != b.Results || a.Results.Delivered == 0 || a.Results.Delivered > a.Results.Recipients {
		t.Fatalf("campaign %+v, again %+v", a, b)
	}
}

func TestSandboxSimulations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var s SandboxMailer
	tests := []struct {
		simulate string
		kind     platform.Kind
		found    bool
	}{
		{"", "", false},
		{"auth_revoked", platform.AuthRevoked, false},
		{"uncertain", platform.Uncertain, true},
	}
	for _, tt := range tests {
		c := platform.Credentials{"simulate": tt.simulate}
		_, err := s.Schedule(ctx, c, Message{Tag: "t"}, []string{"list-1"}, time.Now())
		var kind platform.Kind
		if err != nil {
			kind = platform.KindOf(err)
		}
		if kind != tt.kind {
			t.Errorf("%q: schedule %v, want %q", tt.simulate, err, tt.kind)
		}
		if id, _ := s.Find(ctx, c, "t"); (id != "") != tt.found {
			t.Errorf("%q: found %q", tt.simulate, id)
		}
	}
	if cp, _ := s.Campaign(ctx, platform.Credentials{"simulate": "stopped"}, "x"); cp.Status != CampaignStopped {
		t.Fatalf("stopped: %+v", cp)
	}
	if _, err := s.Schedule(ctx, platform.Credentials{}, Message{}, nil, time.Now()); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("no audience: %v", err)
	}
}
