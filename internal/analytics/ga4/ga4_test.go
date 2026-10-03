// SPDX-License-Identifier: AGPL-3.0-or-later

package ga4

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// serviceKey is a service account's JSON key, as Google gives it.
func serviceKey(t *testing.T) string {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	der, err := x509.MarshalPKCS8PrivateKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "araldo@brand.iam.gserviceaccount.com",
		"private_key_id": "kid1", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	return string(b)
}

// fakeGoogle signs in service accounts whose assertions verify against the
// test key, unless refuse is set, and answers runReport for property 123.
type fakeGoogle struct {
	mu       sync.Mutex
	refuse   bool
	claims   map[string]any
	requests []map[string]any
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[len(parts)-1])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if f.refuse || len(parts) != 3 || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
			rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
			return
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(claims, &f.claims)
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3599,"token_type":"Bearer"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer at" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/v1beta/properties/123:runReport" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"User does not have sufficient permissions for this property.","status":"PERMISSION_DENIED"}}`))
		return
	}
	var q map[string]any
	_ = json.NewDecoder(r.Body).Decode(&q)
	f.requests = append(f.requests, q)
	dims, _ := q["dimensions"].([]any)
	offset, _ := q["offset"].(float64)
	row := func(m string, d ...string) string {
		vals := make([]string, len(d))
		for i, v := range d {
			vals[i] = `{"value":"` + v + `"}`
		}
		return `{"dimensionValues":[` + strings.Join(vals, ",") + `],"metricValues":[` + m + `]}`
	}
	switch {
	case len(dims) == 0:
		_, _ = w.Write([]byte(`{"rows":[{"metricValues":[{"value":"12"}]}],"rowCount":1,"metadata":{"timeZone":"America/Chicago"}}`))
	case len(dims) == 5 && offset == 0: // traffic, first page of 2
		_, _ = w.Write([]byte(`{"rowCount":3,"rows":[` +
			row(`{"value":"300"},{"value":"320"}`, "20261001", "(direct)", "(none)", "(not set)", "(not set)") + `,` +
			row(`{"value":"40"},{"value":"44"}`, "20261001", "bluesky", "social", "release", "post_01") + `]}`))
	case len(dims) == 5:
		_, _ = w.Write([]byte(`{"rowCount":3,"rows":[` + row(`{"value":"25"},{"value":"26"}`, "20261002", "reddit", "paid", "alpha-launch", "(not set)") + `]}`))
	default: // key events
		_, _ = w.Write([]byte(`{"rowCount":1,"rows":[` + row(`{"value":"5"},{"value":"6"}`, "20261001", "sign_up", "bluesky", "social", "release", "post_01") + `]}`))
	}
}

func setup(t *testing.T) (*fakeGoogle, *Source, platform.Credentials) {
	t.Helper()
	f := &fakeGoogle{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := New(srv.Client())
	s.TokenURL, s.APIURL, s.PageSize = srv.URL+"/token", srv.URL, 2
	return f, s, platform.Credentials{"property_id": "123", "service_account_key": serviceKey(t)}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	f, s, c := setup(t)
	site, err := s.Verify(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if site.ID != "123" || site.Timezone != "America/Chicago" {
		t.Fatalf("site %+v", site)
	}
	if f.claims["iss"] != "araldo@brand.iam.gserviceaccount.com" || f.claims["aud"] != TokenURL ||
		f.claims["scope"] != "https://www.googleapis.com/auth/analytics.readonly" {
		t.Fatalf("the assertion's claims %+v", f.claims)
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Parallel()
	_, s, good := setup(t)
	tests := []struct {
		name     string
		creds    platform.Credentials
		kind     platform.Kind
		code     string
		mentions string
	}{
		{"a measurement ID", platform.Credentials{"property_id": "G-ABC123", "service_account_key": good["service_account_key"]},
			platform.Rejected, "property_invalid", "not a G- measurement ID"},
		{"not JSON", platform.Credentials{"property_id": "123", "service_account_key": "-----BEGIN PRIVATE KEY-----"},
			platform.Rejected, "key_invalid", "JSON"},
		{"an OAuth client file", platform.Credentials{"property_id": "123", "service_account_key": `{"installed":{"client_id":"x"}}`},
			platform.Rejected, "key_invalid", "service account"},
		{"a property the account is not on", platform.Credentials{"property_id": "456", "service_account_key": good["service_account_key"]},
			platform.Rejected, "no_access", "Viewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := s.Verify(t.Context(), tt.creds)
			var pe *platform.Error
			if !errors.As(err, &pe) || pe.Kind != tt.kind || pe.Code != tt.code || !strings.Contains(pe.Msg, tt.mentions) {
				t.Fatalf("got %#v, want %v %s mentioning %q", err, tt.kind, tt.code, tt.mentions)
			}
		})
	}
}

func TestReport(t *testing.T) {
	t.Parallel()
	f, s, c := setup(t)
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rows, err := s.Report(t.Context(), c, from, to, []string{"sign_up"})
	if err != nil {
		t.Fatal(err)
	}
	want := []analytics.Row{
		{Day: from, Visitors: 300, Visits: 320},
		{Day: from, UTM: analytics.UTM{Source: "bluesky", Medium: "social", Campaign: "release", Content: "post_01"}, Visitors: 40, Visits: 44},
		{Day: to, UTM: analytics.UTM{Source: "reddit", Medium: "paid", Campaign: "alpha-launch"}, Visitors: 25, Visits: 26},
		{Day: from, Goal: "sign_up", UTM: analytics.UTM{Source: "bluesky", Medium: "social", Campaign: "release", Content: "post_01"}, Visitors: 5, Events: 6},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d requests, want 3 (two pages of traffic, one of key events)", len(f.requests))
	}
	filter, _ := json.Marshal(f.requests[2]["dimensionFilter"])
	if !strings.Contains(string(filter), `"fieldName":"eventName"`) || !strings.Contains(string(filter), `"values":["sign_up"]`) {
		t.Fatalf("key events are filtered by name: %s", filter)
	}
	ranges, _ := json.Marshal(f.requests[0]["dateRanges"])
	if string(ranges) != `[{"endDate":"2026-10-02","startDate":"2026-10-01"}]` {
		t.Fatalf("date range %s", ranges)
	}
}

// Once connected, a deleted key or a service account taken off the
// property asks for the source to be connected again.
func TestReportLosingAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		lose func(*fakeGoogle, platform.Credentials)
		code string
	}{
		{"key deleted", func(f *fakeGoogle, _ platform.Credentials) { f.refuse = true }, "key_refused"},
		{"removed from the property", func(_ *fakeGoogle, c platform.Credentials) { c["property_id"] = "456" }, "no_access"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, s, c := setup(t)
			tt.lose(f, c)
			_, err := s.Report(t.Context(), c, time.Now(), time.Now(), nil)
			var pe *platform.Error
			if !errors.As(err, &pe) || pe.Kind != platform.AuthRevoked || pe.Code != tt.code {
				t.Fatalf("got %#v, want AuthRevoked %s", err, tt.code)
			}
		})
	}
}
