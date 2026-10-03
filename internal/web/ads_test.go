// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import "testing"

func TestMoney(t *testing.T) {
	t.Parallel()
	tests := []struct {
		minor    int64
		currency string
		want     string
	}{
		{123456, "USD", "1234.56 USD"},
		{5, "EUR", "0.05 EUR"},
		{0, "USD", "0.00 USD"},
		{-250, "USD", "-2.50 USD"},
		{1500, "JPY", "1500 JPY"},
	}
	for _, tt := range tests {
		if got := money(tt.minor, tt.currency); got != tt.want {
			t.Errorf("money(%d, %s) = %q, want %q", tt.minor, tt.currency, got, tt.want)
		}
	}
}
