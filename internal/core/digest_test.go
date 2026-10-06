// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"
)

func TestNextDigest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		now  string
		tz   string
		want string
	}{
		{"2026-11-10T13:59:00Z", "America/Denver", "2026-11-10T15:00:00Z"}, // 06:59 there: 08:00 today
		{"2026-11-10T15:00:00Z", "America/Denver", "2026-11-11T15:00:00Z"}, // 08:00 there exactly: tomorrow
		{"2026-11-10T20:00:00Z", "Europe/Rome", "2026-11-11T07:00:00Z"},    // 21:00 there
		{"2026-03-08T12:00:00Z", "America/Denver", "2026-03-08T14:00:00Z"}, // the day DST starts: 06:00 MDT, then 08:00 MDT
		{"2026-11-10T07:59:00Z", "Not/AZone", "2026-11-10T08:00:00Z"},      // an unknown zone is UTC
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextDigest(now, tc.tz).UTC().Format(time.RFC3339); got != tc.want {
			t.Errorf("nextDigest(%s, %s) = %s, want %s", tc.now, tc.tz, got, tc.want)
		}
	}
}
