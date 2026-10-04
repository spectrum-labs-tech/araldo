// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"
)

func TestParsePublishAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in       string
		want     time.Time // zero for next_slot
		slot     bool
		rejected bool
	}{
		{in: "now", want: now},
		{in: "", want: now},
		{in: "next_slot", slot: true},
		{in: "2026-10-06T09:00:00Z", want: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)},
		// A little late means now, for a late clock.
		{in: "2026-10-05T11:50:00Z", want: now},
		// Older is a mistake, not "publish at once".
		{in: "2026-10-05T11:30:00Z", rejected: true},
		{in: "2020-06-01T15:00:00Z", rejected: true},
		{in: "2028-01-01T00:00:00Z", rejected: true},
		{in: "tomorrow", rejected: true},
	}
	for _, tt := range tests {
		at, slot, err := parsePublishAt(tt.in, now)
		switch {
		case tt.rejected:
			if err == nil {
				t.Errorf("%q: accepted, want refused", tt.in)
			}
		case err != nil:
			t.Errorf("%q: %v", tt.in, err)
		case slot != tt.slot:
			t.Errorf("%q: slot %v", tt.in, slot)
		case !tt.slot && (at == nil || !at.Equal(tt.want)):
			t.Errorf("%q: %v, want %v", tt.in, at, tt.want)
		}
	}
}
