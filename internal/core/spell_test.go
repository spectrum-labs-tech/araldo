// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"
)

func TestSpell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{InvitationTTL, "7 days"},
		{PasswordResetTTL, "30 minutes"},
		{24 * time.Hour, "1 day"},
		{time.Hour, "1 hour"},
		{36 * time.Hour, "36 hours"},
		{90 * time.Minute, "90 minutes"},
		{time.Minute, "1 minute"},
	}
	for _, tt := range tests {
		if got := spell(tt.d); got != tt.want {
			t.Errorf("spell(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
