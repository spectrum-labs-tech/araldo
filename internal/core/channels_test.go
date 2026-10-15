// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestConnectError(t *testing.T) {
	t.Parallel()
	other := errors.New("boom")
	tests := []struct {
		name     string
		err      error
		kind     apperr.Kind
		code     string
		passThru bool
	}{
		{"refused credentials", &platform.Error{Kind: platform.AuthRevoked, Msg: "bad token"}, apperr.KindInvalid, "connect_failed", false},
		{"rejected", &platform.Error{Kind: platform.Rejected, Msg: "no"}, apperr.KindInvalid, "connect_failed", false},
		{"unreachable", &platform.Error{Kind: platform.Transient}, apperr.KindUnavailable, "provider_unavailable", false},
		{"rate limited", &platform.Error{Kind: platform.RateLimited}, apperr.KindUnavailable, "provider_unavailable", false},
		{"unsure", &platform.Error{Kind: platform.Uncertain}, apperr.KindUnavailable, "provider_unavailable", false},
		{"not a platform error", other, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := connectError(platform.Bluesky, tt.err)
			if tt.passThru {
				if !errors.Is(got, other) {
					t.Fatalf("got %v, want the error itself", got)
				}
				return
			}
			var ae *apperr.Error
			if !errors.As(got, &ae) || ae.Kind != tt.kind || ae.Code != tt.code {
				t.Fatalf("got %#v, want kind %v code %s", got, tt.kind, tt.code)
			}
		})
	}
}

func TestMergeCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		settings, secrets map[string]string
		want              platform.Credentials
	}{
		{"both", map[string]string{"instance": "a.example"}, map[string]string{"token": "t"},
			platform.Credentials{"instance": "a.example", "token": "t"}},
		{"secrets win", map[string]string{"token": "plain"}, map[string]string{"token": "secret"}, platform.Credentials{"token": "secret"}},
		{"neither", nil, nil, platform.Credentials{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mergeCredentials(tt.settings, tt.secrets); !maps.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCallProviderSetsADeadline checks a provider call gets a deadline even
// when its caller set none: the shared HTTP client has no overall one.
func TestCallProviderSetsADeadline(t *testing.T) {
	t.Parallel()
	got, err := callProvider(context.Background(), func(ctx context.Context) (time.Duration, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			return 0, errors.New("no deadline")
		}
		return time.Until(dl), nil
	})
	if err != nil || got <= 0 || got > providerCallTimeout {
		t.Fatalf("deadline in %v, %v; want within %v", got, err, providerCallTimeout)
	}
}
