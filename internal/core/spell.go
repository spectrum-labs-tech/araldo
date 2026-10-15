// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strconv"
	"time"
)

// spell writes a whole number of days, hours or minutes as people do in
// email: "7 days", "1 hour", "30 minutes".
func spell(d time.Duration) string {
	unit, n := "minute", int64(d/time.Minute)
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		unit, n = "day", int64(d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		unit, n = "hour", int64(d/time.Hour)
	}
	if n != 1 {
		unit += "s"
	}
	return strconv.FormatInt(n, 10) + " " + unit
}
