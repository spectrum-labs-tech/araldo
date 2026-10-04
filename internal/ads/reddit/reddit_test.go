// SPDX-License-Identifier: AGPL-3.0-or-later

package reddit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeReddit is Reddit's sign-in and Ads API for one business with two ad
// accounts.
type fakeReddit struct {
	mu      sync.Mutex
	srv     *httptest.Server
	reports []map[string]any
	evil    bool // send a next page off the Ads API
}

func (f *fakeReddit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/auth/") {
		f.token(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer access" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	page := r.URL.Query().Get("page")
	switch {
	case r.URL.Path == "/api/me/businesses":
		_, _ = w.Write([]byte(`{"data":[{"id":"biz1","name":"Spectrum Labs"}],"pagination":{"next_url":null}}`))
	case r.URL.Path == "/api/businesses/biz1/ad_accounts" && page == "":
		_, _ = w.Write([]byte(`{"data":[{"id":"t2_openb00ks","name":"Open B00KS","currency":"USD","time_zone_id":"America/Los_Angeles"}],` +
			`"pagination":{"next_url":"` + f.srv.URL + `/api/businesses/biz1/ad_accounts?page=2"}}`))
	case r.URL.Path == "/api/businesses/biz1/ad_accounts":
		_, _ = w.Write([]byte(`{"data":[{"id":"t2_araldo","name":"Araldo","currency":"EUR","time_zone_id":"Europe/Berlin"}],"pagination":{}}`))
	case r.URL.Path == "/api/ad_accounts/t2_openb00ks":
		_, _ = w.Write([]byte(`{"data":{"id":"t2_openb00ks","name":"Open B00KS","currency":"usd","time_zone_id":"America/Los_Angeles"}}`))
	case r.URL.Path == "/api/ad_accounts/t2_openb00ks/campaigns":
		_, _ = w.Write([]byte(`{"data":[{"id":"c1","name":"r/LocalLLaMA launch"}],"pagination":{"next_url":null}}`))
	case r.URL.Path == "/api/ad_accounts/t2_openb00ks/reports" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.reports = append(f.reports, body)
		if page == "" {
			next := f.srv.URL + "/api/ad_accounts/t2_openb00ks/reports?page=2"
			if f.evil {
				next = "https://evil.example/steal"
			}
			_, _ = w.Write([]byte(`{"data":{"metrics":[{"date":"2026-10-01T00:00:00Z","campaign_id":"c1","spend":12345678,"impressions":4000,"clicks":51}]},` +
				`"pagination":{"next_url":"` + next + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"metrics":[{"date":"2026-10-02","campaign_id":"c9","spend":500000.0,"impressions":10,"clicks":0}]},"pagination":{}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeReddit) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	user, pass, _ := r.BasicAuth()
	form := r.PostForm
	switch {
	case user != "id" || pass != "secret":
		w.WriteHeader(http.StatusUnauthorized)
	case form.Get("grant_type") == "authorization_code" && form.Get("code") == "c":
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":"adsread"}`))
	case form.Get("grant_type") == "refresh_token" && form.Get("refresh_token") == "refresh":
		_, _ = w.Write([]byte(`{"access_token":"access","expires_in":3600,"scope":"adsread"}`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}
}

func setup(t *testing.T) (*fakeReddit, *Network) {
	t.Helper()
	f := &fakeReddit{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	n := New(f.srv.Client())
	n.API, n.Auth = f.srv.URL+"/api", f.srv.URL+"/auth"
	return f, n
}

var app = platform.App{ClientID: "id", ClientSecret: "secret"}

func TestSignIn(t *testing.T) {
	t.Parallel()
	_, n := setup(t)
	u, _ := url.Parse(n.AuthorizeURL(app, "https://araldo.test/connect/reddit_ads/callback", "st", ""))
	q := u.Query()
	if q.Get("duration") != "permanent" || q.Get("scope") != "adsread" || q.Get("state") != "st" || q.Get("response_type") != "code" {
		t.Fatalf("authorize URL %s: read-only, with a permanent refresh token", u)
	}
	conns, err := n.Exchange(t.Context(), app, "https://araldo.test/cb", "c", "")
	if err != nil || len(conns) != 2 {
		t.Fatalf("Exchange = %+v, %v (both pages of accounts)", conns, err)
	}
	if c := conns[0]; c.Account.ExternalID != "t2_openb00ks" || c.Account.DisplayName != "Open B00KS" || c.Credentials["refresh_token"] != "refresh" ||
		c.Credentials["account_id"] != "t2_openb00ks" {
		t.Fatalf("connection %+v", c)
	}
	if _, err := n.Exchange(t.Context(), platform.App{ClientID: "id", ClientSecret: "wrong"}, "x", "c", ""); err == nil {
		t.Fatal("a wrong app secret should fail")
	}
}

func TestVerifyAndReport(t *testing.T) {
	t.Parallel()
	f, n := setup(t)
	creds := platform.Credentials{"refresh_token": "refresh", "account_id": "t2_openb00ks"}
	acct, err := n.Verify(t.Context(), app, creds)
	if err != nil || acct.Currency != "USD" || acct.Timezone != "America/Los_Angeles" || acct.Name != "Open B00KS" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	results, err := n.Report(t.Context(), app, creds, from, to)
	if err != nil || len(results) != 2 {
		t.Fatalf("Report = %+v, %v (both pages)", results, err)
	}
	// 12,345,678 micro-dollars is $12.35.
	if r := results[0]; r.CampaignName != "r/LocalLLaMA launch" || r.Spend != 1235 || r.Clicks != 51 || r.Impressions != 4000 ||
		!r.Day.Equal(from) || r.Results != 0 {
		t.Fatalf("first row %+v", r)
	}
	if r := results[1]; r.CampaignName != "c9" || r.Spend != 50 || !r.Day.Equal(to) {
		t.Fatalf("a campaign missing from the list keeps its ID as its name: %+v", r)
	}
	data, _ := f.reports[0]["data"].(map[string]any)
	if data["starts_at"] != "2026-10-01T00:00:00Z" || data["ends_at"] != "2026-10-03T00:00:00Z" || data["time_zone_id"] != "America/Los_Angeles" ||
		len(data["breakdowns"].([]any)) != 2 {
		t.Fatalf("report request %v: the whole last day, in the account's zone", data)
	}

	if _, err := n.Report(t.Context(), app, platform.Credentials{"refresh_token": "spent", "account_id": "t2_openb00ks"}, from, to); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a refused refresh token: %v", err)
	}
	f.evil = true
	if _, err := n.Report(t.Context(), app, creds, from, to); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a next page off the Ads API must not get the token: %v", err)
	}
}

func TestMinorUnits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		micros   string
		currency string
		want     int64
	}{
		{"12345678", "USD", 1235},
		{"0", "USD", 0},
		{"1500000000", "JPY", 1500},
		{"4999", "EUR", 0},
		{"5000", "EUR", 1},
	} {
		if got := minorUnits(json.Number(tt.micros), tt.currency); got != tt.want {
			t.Errorf("minorUnits(%s, %s) = %d, want %d", tt.micros, tt.currency, got, tt.want)
		}
	}
}
