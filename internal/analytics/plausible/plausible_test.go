// SPDX-License-Identifier: AGPL-3.0-or-later

package plausible

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakePlausible answers /api/v2/query for example.com with key k.
type fakePlausible struct {
	mu      sync.Mutex
	queries []map[string]any
}

func (f *fakePlausible) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/api/v2/query" || r.Header.Get("Authorization") != "Bearer k" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Invalid API key"}`))
		return
	}
	var q map[string]any
	_ = json.NewDecoder(r.Body).Decode(&q)
	f.queries = append(f.queries, q)
	if q["site_id"] != "example.com" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Site does not exist"}`))
		return
	}
	dims, _ := q["dimensions"].([]any)
	offset := q["pagination"].(map[string]any)["offset"].(float64)
	switch {
	case len(dims) == 0:
		_, _ = w.Write([]byte(`{"results":[{"metrics":[12],"dimensions":[]}]}`))
	case len(dims) == 5 && offset == 0: // traffic, first page of 2
		_, _ = w.Write([]byte(`{"results":[` +
			`{"dimensions":["2026-10-01","(none)","(none)","(none)","(none)"],"metrics":[300,320]},` +
			`{"dimensions":["2026-10-01","bluesky","social","release","post_01"],"metrics":[40,44]}]}`))
	case len(dims) == 5:
		_, _ = w.Write([]byte(`{"results":[{"dimensions":["2026-10-02","reddit","paid","alpha-launch",null],"metrics":[25,26]}]}`))
	default: // conversions
		_, _ = w.Write([]byte(`{"results":[{"dimensions":["2026-10-01","Waitlist Signup","bluesky","social","release","post_01"],"metrics":[5,6]}]}`))
	}
}

func setup(t *testing.T) (*fakePlausible, *Source, platform.Credentials) {
	t.Helper()
	f := &fakePlausible{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := New(srv.Client())
	s.PageSize = 2
	return f, s, platform.Credentials{"site_id": "example.com", "api_key": "k", "base_url": srv.URL}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	_, s, creds := setup(t)
	if site, err := s.Verify(t.Context(), creds); err != nil || site.ID != "example.com" {
		t.Fatalf("Verify = %+v, %v", site, err)
	}
	bad := platform.Credentials{"site_id": "example.com", "api_key": "wrong", "base_url": creds["base_url"]}
	if _, err := s.Verify(t.Context(), bad); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a wrong key: %v", err)
	}
	if _, err := s.Verify(t.Context(), platform.Credentials{"site_id": "x", "api_key": "k", "base_url": "ftp://x"}); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a bad URL: %v", err)
	}
}

func TestReport(t *testing.T) {
	t.Parallel()
	f, s, creds := setup(t)
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rows, err := s.Report(t.Context(), creds, from, to, []string{"Waitlist Signup"})
	if err != nil || len(rows) != 4 {
		t.Fatalf("Report = %+v, %v (two traffic pages and the conversions)", rows, err)
	}
	if r := rows[0]; r.UTM != (analytics.UTM{}) || r.Visitors != 300 || r.Visits != 320 || !r.Day.Equal(from) {
		t.Fatalf("untagged traffic %+v: \"(none)\" means no tag", r)
	}
	if r := rows[1]; r.UTM.Content != "post_01" || r.UTM.Source != "bluesky" || r.Visitors != 40 {
		t.Fatalf("tagged traffic %+v", r)
	}
	if r := rows[2]; r.UTM.Campaign != "alpha-launch" || r.UTM.Content != "" || !r.Day.Equal(to) {
		t.Fatalf("second page %+v: a null tag is no tag", r)
	}
	if r := rows[3]; r.Goal != "Waitlist Signup" || r.Visitors != 5 || r.Events != 6 || r.UTM.Content != "post_01" {
		t.Fatalf("conversions %+v", r)
	}
	goal := f.queries[len(f.queries)-1]
	if filters, _ := goal["filters"].([]any); len(filters) != 1 || goal["date_range"].([]any)[0] != "2026-10-01" {
		t.Fatalf("conversion query %v: filtered to the goals, over the window", goal)
	}
}
