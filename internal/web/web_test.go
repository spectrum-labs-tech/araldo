// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"slices"
	"testing"
	"time"
)

func TestKeyExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		choice, on string
		want       *time.Time
		wantErr    bool
	}{
		{"never", "", nil, false},
		{"", "", nil, false},
		{"30d", "", ptrTime(now.AddDate(0, 0, 30)), false},
		{"1y", "", ptrTime(now.AddDate(1, 0, 0)), false},
		{"date", "2026-12-31", ptrTime(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)), false},
		{"date", "2026-01-01", nil, true}, // in the past
		{"date", "not a date", nil, true},
		{"forever", "", nil, true},
	}
	for _, tt := range tests {
		got, err := keyExpiry(tt.choice, tt.on, now)
		if (err != nil) != tt.wantErr {
			t.Errorf("keyExpiry(%q, %q) err = %v", tt.choice, tt.on, err)
			continue
		}
		if (got == nil) != (tt.want == nil) || got != nil && !got.Equal(*tt.want) {
			t.Errorf("keyExpiry(%q, %q) = %v, want %v", tt.choice, tt.on, got, tt.want)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSlotsRoundTrip(t *testing.T) {
	t.Parallel()
	slots, err := parseSlots("weekdays 09:00\nsat 18:30\n\nMonday 13:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 7 {
		t.Fatalf("got %d slots, want 7", len(slots))
	}
	if got := formatSlots(slots); got != "mon 09:00\nmon 13:00\ntue 09:00\nwed 09:00\nthu 09:00\nfri 09:00\nsat 18:30" {
		t.Fatalf("formatSlots = %q", got)
	}
	for _, bad := range []string{"mon", "mon 25:00", "someday 09:00"} {
		if _, err := parseSlots(bad); err == nil {
			t.Errorf("parseSlots(%q) accepted", bad)
		}
	}
	if s, err := parseSlots("daily 07:05"); err != nil || len(s) != 7 || s[0].MinuteOfDay != 7*60+5 || s[0].Weekday != time.Sunday {
		t.Errorf("daily = %+v, %v", s, err)
	}
}

func TestZoneGroups(t *testing.T) {
	t.Parallel()
	if zoneGroups[0].Region != "UTC" {
		t.Fatalf("first group %q, want UTC", zoneGroups[0].Region)
	}
	var all []string
	for _, g := range zoneGroups {
		all = append(all, g.Zones...)
	}
	for _, want := range []string{"America/Denver", "America/New_York", "Europe/Rome", "Asia/Tokyo", "Australia/Sydney"} {
		if !slices.Contains(all, want) {
			t.Errorf("zone list lacks %s", want)
		}
	}
	for _, z := range all {
		if _, err := time.LoadLocation(z); err != nil {
			t.Errorf("listed zone %s does not load: %v", z, err)
		}
	}
}

func TestSafeNext(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                     "/",
		"/posts":               "/posts",
		"//evil.example/x":     "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"posts":                "/",
		"/\t/evil.example":     "/",
		"/\n/evil.example":     "/",
		`/x\..\evil`:           "/",
		"/posts?q=a%20b#top":   "/posts?q=a%20b#top",
		"/%09/evil.example":    "/%09/evil.example", // encoded, it stays a path
	}
	for in, want := range tests {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
