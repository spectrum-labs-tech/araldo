// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestReportPeriod(t *testing.T) {
	t.Parallel()
	b := &model.Brand{Timezone: "America/Denver"}
	// 03:00 UTC on October 3 is still October 2 in Denver.
	now := time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name         string
		in           ReportInput
		since, until time.Time
		invalid      bool
	}{
		{"the last full month by default", ReportInput{}, day(2026, 9, 1), day(2026, 9, 30), false},
		{"a month", ReportInput{Month: "2026-02"}, day(2026, 2, 1), day(2026, 2, 28), false},
		{"the current month, so far and beyond", ReportInput{Month: "2026-10"}, day(2026, 10, 1), day(2026, 10, 31), false},
		{"a range", ReportInput{Since: day(2026, 9, 10), Until: day(2026, 9, 16)}, day(2026, 9, 10), day(2026, 9, 16), false},
		{"a range up to today in the brand's zone", ReportInput{Since: day(2026, 9, 20)}, day(2026, 9, 20), day(2026, 10, 2), false},
		{"not a month", ReportInput{Month: "2026-13"}, time.Time{}, time.Time{}, true},
		{"backwards", ReportInput{Since: day(2026, 9, 2), Until: day(2026, 9, 1)}, time.Time{}, time.Time{}, true},
		{"more than a year", ReportInput{Since: day(2025, 1, 1), Until: day(2026, 1, 2)}, time.Time{}, time.Time{}, true},
		{"not started", ReportInput{Month: "2026-11"}, time.Time{}, time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			since, until, err := reportPeriod(tt.in, b, now)
			if tt.invalid {
				if apperr.As(err).Kind != apperr.KindInvalid {
					t.Fatalf("got %v, want invalid", err)
				}
				return
			}
			if err != nil || !since.Equal(tt.since) || !until.Equal(tt.until) {
				t.Fatalf("got %v to %v (%v), want %v to %v", since, until, err, tt.since, tt.until)
			}
		})
	}
}

func TestReportWindowIsTheBrandsDays(t *testing.T) {
	t.Parallel()
	r := &Report{Brand: &model.Brand{Timezone: "America/Denver"}}
	from, to := r.window(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	if from.UTC() != time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC) || to.UTC() != time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC) {
		t.Fatalf("window %v to %v", from.UTC(), to.UTC())
	}
}
