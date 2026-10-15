// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
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
	for _, bad := range []string{"mon", "mon 25:00", "someday 09:00", "mo: 09:00", ": 09:00", "m 09:00"} {
		if _, err := parseSlots(bad); err == nil {
			t.Errorf("parseSlots(%q) accepted", bad)
		}
	}
	if s, err := parseSlots("tue: 08:15"); err != nil || len(s) != 1 || s[0].Weekday != time.Tuesday {
		t.Errorf("a day with a colon = %+v, %v", s, err)
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

func TestBrandLoc(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		brand *model.Brand
		want  string
	}{
		{"no brand", nil, "UTC"},
		{"no zone", &model.Brand{}, "UTC"},
		{"unknown zone", &model.Brand{Timezone: "Mars/Olympus"}, "UTC"},
		{"a zone", &model.Brand{Timezone: "America/New_York"}, "America/New_York"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := brandLoc(tt.brand).String(); got != tt.want {
				t.Fatalf("brandLoc = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestServerFault checks how signed-out pages answer errors: a dependency
// down is 503 with Retry-After, anything unexpected 500, and the visitor's
// own errors are left to the page.
func TestServerFault(t *testing.T) {
	t.Parallel()
	s := &Server{log: slog.New(slog.DiscardHandler)}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantFault  bool
		wantRetry  bool
	}{
		{"no error", nil, 0, false, false},
		{"unavailable", &apperr.Error{Kind: apperr.KindUnavailable, Message: "The database is down."}, http.StatusServiceUnavailable, true, true},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError, true, false},
		{"not found", apperr.NotFound("invitation"), 0, false, false},
		{"refused", apperr.Forbidden("No."), 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			status, msg, fault := s.serverFault(w, httptest.NewRequest(http.MethodGet, "/login", nil), "testing", tt.err)
			if status != tt.wantStatus || fault != tt.wantFault || (fault && msg == "") {
				t.Fatalf("serverFault = %d, %q, %v; want %d, fault %v", status, msg, fault, tt.wantStatus, tt.wantFault)
			}
			if got := w.Header().Get("Retry-After") != ""; got != tt.wantRetry {
				t.Fatalf("Retry-After set = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}
