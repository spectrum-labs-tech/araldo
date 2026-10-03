// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func TestCostPer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		spend, n, want int64
	}{
		{spend: 1000, n: 4, want: 250},
		{spend: 1000, n: 3, want: 333},
		{spend: 1001, n: 2, want: 501}, // rounds half up
		{spend: 1000, n: 0, want: 0},
		{spend: 0, n: 5, want: 0},
	}
	for _, tt := range tests {
		if got := CostPer(tt.spend, tt.n); got != tt.want {
			t.Errorf("CostPer(%d, %d) = %d, want %d", tt.spend, tt.n, got, tt.want)
		}
	}
}
