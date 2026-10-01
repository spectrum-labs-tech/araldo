// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestHistoryOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "success", err: nil, want: "published"},
		{name: "classified", err: &platform.Error{Kind: platform.RateLimited}, want: "rate_limited"},
		{name: "classified and wrapped", err: fmt.Errorf("posting: %w", &platform.Error{Kind: platform.Rejected}), want: "rejected"},
		{name: "unclassified", err: errors.New("adapter bug"), want: "unknown"},
	}
	for _, tt := range tests {
		if got := historyOutcome(tt.err); got != tt.want {
			t.Errorf("%s: historyOutcome = %q, want %q", tt.name, got, tt.want)
		}
	}
}
