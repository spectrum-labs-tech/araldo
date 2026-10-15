// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
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

func TestChooseBrand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode string // "" when the error must pass through unchanged
	}{
		{"no such brand", apperr.NotFound("brand"), "brand_required"},
		{"no brand given", apperr.Invalid("brand_invalid", "brand", "Bad."), "brand_required"},
		{"database down", &apperr.Error{Kind: apperr.KindUnavailable, Code: "service_unavailable"}, ""},
		{"refused", apperr.Forbidden("This key is for another brand."), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := chooseBrand(tt.err)
			if tt.wantCode == "" {
				if !errors.Is(got, tt.err) {
					t.Fatalf("chooseBrand changed %v into %v", tt.err, got)
				}
				return
			}
			if ae := apperr.As(got); ae.Code != tt.wantCode || ae.Kind != apperr.KindInvalid {
				t.Fatalf("chooseBrand = %+v, want an invalid %s", ae, tt.wantCode)
			}
		})
	}
}

// TestNotice checks that a notice reaches the next page once, through a
// cookie, and that one in the address is never shown.
func TestNotice(t *testing.T) {
	t.Parallel()
	set := httptest.NewRecorder()
	setNotice(set, true, "Saved: Spring launch.")
	cks := set.Result().Cookies()
	if len(cks) != 1 || !cks[0].HttpOnly || !cks[0].Secure || cks[0].MaxAge <= 0 {
		t.Fatalf("the notice cookie: %+v", cks)
	}
	r := httptest.NewRequest(http.MethodGet, "/?notice=Your+account+is+locked", nil)
	r.AddCookie(cks[0])
	w := httptest.NewRecorder()
	if got := takeNotice(w, r, true); got != "Saved: Spring launch." {
		t.Fatalf("takeNotice = %q", got)
	}
	if cleared := w.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("the notice was not cleared: %+v", cleared)
	}
	if got := takeNotice(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?notice=Your+account+is+locked", nil), true); got != "" {
		t.Fatalf("a notice in the address was shown: %q", got)
	}
	bad := httptest.NewRequest(http.MethodGet, "/", nil)
	bad.AddCookie(&http.Cookie{Name: noticeCookie, Value: "not base64!"})
	if got := takeNotice(httptest.NewRecorder(), bad, true); got != "" {
		t.Fatalf("a malformed notice was shown: %q", got)
	}
	empty := httptest.NewRecorder()
	setNotice(empty, true, "")
	if len(empty.Result().Cookies()) != 0 {
		t.Fatal("an empty notice set a cookie")
	}
}

func TestConnectRefused(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"access_denied", "server_error", "<script>alert(1)</script>", ""} {
		if got := connectRefused(code); strings.Contains(got, code) && code != "" || !strings.HasPrefix(got, "Not connected:") {
			t.Errorf("connectRefused(%q) = %q", code, got)
		}
	}
}

func TestSameToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got, want string
		same      bool
	}{
		{"abc", "abc", true},
		{"abd", "abc", false},
		{"ab", "abc", false},
		{"", "", false}, // a session without a token never matches an empty field
		{"", "abc", false},
	}
	for _, tt := range tests {
		if got := sameToken(tt.got, tt.want); got != tt.same {
			t.Errorf("sameToken(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.same)
		}
	}
}

func TestIPLimiter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	l := newIPLimiter(2, time.Minute)
	l.tracked = 3
	for i, want := range []bool{true, true, false} {
		if got := l.allow("a", now); got != want {
			t.Fatalf("attempt %d from a = %v, want %v", i+1, got, want)
		}
	}
	if !l.allow("b", now) || !l.allow("c", now) {
		t.Fatal("other addresses are held back")
	}
	// Full, and nothing has expired: a new address waits, and a is still
	// held back instead of everyone's count being reset.
	if l.allow("d", now) {
		t.Fatal("a new address got in while the limiter was full")
	}
	if l.allow("a", now.Add(time.Second)) {
		t.Fatal("a's count was reset")
	}
	// A window later the old addresses are forgotten and room is made.
	later := now.Add(2 * time.Minute)
	if !l.allow("d", later) || !l.allow("a", later) {
		t.Fatal("expired addresses were not forgotten")
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		header, value, want string
	}{
		{"", "", "192.0.2.1"},
		{"CF-Connecting-IP", "203.0.113.7", "203.0.113.7"},
		{"X-Forwarded-For", "198.51.100.9, 203.0.113.7", "203.0.113.7"}, // the client wrote the first
		{"X-Forwarded-For", "", "192.0.2.1"},
	}
	for _, tt := range tests {
		s := &Server{cfg: Config{ClientIPHeader: tt.header}}
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = "192.0.2.1:4242"
		if tt.header != "" && tt.value != "" {
			r.Header.Set(tt.header, tt.value)
		}
		if got := s.clientIP(r); got != tt.want {
			t.Errorf("clientIP with %s %q = %q, want %q", tt.header, tt.value, got, tt.want)
		}
	}
}

func TestProviderFields(t *testing.T) {
	t.Parallel()
	defs := []platform.Field{{Name: "handle"}, {Name: "app_password", Secret: true}, {Name: "instance", Optional: true}}
	form := url.Values{"field_handle": {"araldo.dev"}, "field_app_password": {"hunter2"}, "field_unknown": {"x"}, "brand": {"brand_1"}}
	fields, values := providerFields(form, defs)
	if len(fields) != 2 || fields["handle"] != "araldo.dev" || fields["app_password"] != "hunter2" {
		t.Fatalf("fields = %v; want the two declared ones sent, nothing undeclared", fields)
	}
	if len(values) != 1 || values["handle"] != "araldo.dev" {
		t.Fatalf("values = %v; want only the non-secret one, to show again", values)
	}
	if f, v := providerFields(form, nil); len(f) != 0 || len(v) != 0 {
		t.Fatalf("no declared fields read %v, %v", f, v)
	}
}

func TestStatusClass(t *testing.T) {
	t.Parallel()
	for class, statuses := range map[string][]any{
		"good":    {model.PostPublished, model.ChannelActive, model.IssueSent, model.EndpointEnabled, "succeeded", "ok"},
		"bad":     {model.PostFailed, model.TargetNeedsAttention, model.ChannelNeedsReauth, model.EndpointDisabled, model.DeliveryNeedsAttention},
		"warn":    {model.PostPendingApproval, model.DeliveryHeld, model.IssuePartiallySent, model.DeliveryHandedOff, "delivering"},
		"neutral": {model.PostScheduled, model.IssueDraft, model.TargetCanceled, "something new"},
	} {
		for _, st := range statuses {
			if got := statusClass(st); got != class {
				t.Errorf("statusClass(%v) = %s, want %s", st, got, class)
			}
		}
	}
}

func TestChangeClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		now, before int64
		lowerBetter bool
		want        string
	}{
		{10, 10, false, "neutral"},
		{12, 10, false, "good"},
		{8, 10, false, "bad"},
		{12, 10, true, "bad"}, // more failures
		{8, 10, true, "good"},
	}
	for _, tt := range tests {
		if got := changeClass(core.Pair{Now: tt.now, Before: tt.before}, tt.lowerBetter); got != tt.want {
			t.Errorf("changeClass(%d from %d, lowerBetter %v) = %s, want %s", tt.now, tt.before, tt.lowerBetter, got, tt.want)
		}
	}
}

func TestCountLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		counts    map[model.PostStatus]int
		total     string
		published string
	}{
		{"under the cap", map[model.PostStatus]int{model.PostPublished: 3, model.PostFailed: 1}, "4", "3"},
		{"at the cap", map[model.PostStatus]int{model.PostPublished: core.PostCountCap}, "1000", "1000"},
		{"past the cap", map[model.PostStatus]int{model.PostPublished: 990, model.PostFailed: 11}, "1000+", "990+"},
		{"one status past it", map[model.PostStatus]int{model.PostPublished: core.PostCountCap + 1}, "1000+", "1000+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			total, labels := countLabels(tt.counts)
			if total != tt.total || labels[model.PostPublished] != tt.published {
				t.Fatalf("countLabels = %q, published %q; want %q, %q", total, labels[model.PostPublished], tt.total, tt.published)
			}
		})
	}
}
