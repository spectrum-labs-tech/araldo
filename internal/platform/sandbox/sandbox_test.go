// SPDX-License-Identifier: AGPL-3.0-or-later

package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestPublish(t *testing.T) {
	t.Parallel()
	a := New("https://araldo.test/")
	var parts []platform.RemoteRef
	res, err := a.Publish(t.Context(), nil, platform.Payload{Key: "ptgt_1", Parts: []string{"one", "two"}, Attempt: 1},
		func(r platform.RemoteRef) error { parts = append(parts, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://araldo.test/sandbox/ptgt_1" || len(res.Parts) != 2 || len(parts) != 2 {
		t.Fatalf("result %+v, reported %d parts", res, len(parts))
	}
}

func TestPublishResumesAThread(t *testing.T) {
	t.Parallel()
	a := New("https://araldo.test")
	posted := []platform.RemoteRef{{ID: "sbx_k_1"}}
	calls := 0
	res, err := a.Publish(t.Context(), nil, platform.Payload{Key: "k", Parts: []string{"a", "b", "c"}, Posted: posted, Attempt: 2},
		func(platform.RemoteRef) error { calls++; return nil })
	if err != nil || calls != 2 || len(res.Parts) != 3 || res.Parts[0].ID != "sbx_k_1" {
		t.Fatalf("res %+v calls %d err %v", res, calls, err)
	}
}

func TestSimulations(t *testing.T) {
	t.Parallel()
	a := New("https://araldo.test")
	a.Sleep = func(context.Context, time.Duration) error { return nil }
	tests := []struct {
		sim     string
		attempt int
		want    platform.Kind // "" means success
	}{
		{SimRateLimited, 1, platform.RateLimited},
		{SimRateLimited, 2, ""},
		{SimAuthRevoked, 3, platform.AuthRevoked},
		{SimRejected, 1, platform.Rejected},
		{SimTimeoutAfterSend, 1, platform.Uncertain},
		{SimTimeoutAfterSend, 2, ""},
		{SimSlow, 1, ""},
	}
	for _, tt := range tests {
		_, err := a.Publish(t.Context(), nil, platform.Payload{Key: "k", Parts: []string{"x"}, Simulate: tt.sim, Attempt: tt.attempt}, nil)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s attempt %d: %v, want success", tt.sim, tt.attempt, err)
		case tt.want != "" && platform.KindOf(err) != tt.want:
			t.Errorf("%s attempt %d: kind %s, want %s", tt.sim, tt.attempt, platform.KindOf(err), tt.want)
		}
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	a := New("https://araldo.test")
	acct, err := a.Verify(t.Context(), platform.Credentials{"emulates": "x"})
	if err != nil || acct.DisplayName != "Sandbox X" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{"emulates": "myspace"}); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("Verify(myspace) = %v", err)
	}
}
