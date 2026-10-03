// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ga4 reads Google Analytics 4 through the Data API's runReport
// (https://developers.google.com/analytics/devguides/reporting/data/v1):
// users and sessions, and each key event's users and count, by day and
// session UTM tags. It reads counts only.
//
// It signs in as a Google Cloud service account, with the JSON key the
// brand pastes and the account added as a Viewer on the property: no
// OAuth app for the install to register, and nothing that expires.
package ga4

import (
	"context"
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
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

const (
	// TokenURL is Google's OAuth 2.0 token endpoint.
	TokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // G101: an endpoint, not a credential
	// APIURL is the Data API.
	APIURL = "https://analyticsdata.googleapis.com"
	scope  = "https://www.googleapis.com/auth/analytics.readonly"
)

// Source reads GA4.
type Source struct {
	Client *http.Client
	// TokenURL and APIURL are Google's; tests point them elsewhere.
	TokenURL, APIURL string
	// PageSize is the rows asked for per request; tests make it small.
	PageSize int
	Now      func() time.Time
}

// New returns the GA4 source.
func New(client *http.Client) *Source {
	return &Source{Client: client, TokenURL: TokenURL, APIURL: APIURL, PageSize: 100000, Now: time.Now}
}

func (s *Source) Provider() analytics.Provider { return "ga4" }
func (s *Source) Name() string                 { return "Google Analytics 4" }

func (s *Source) Fields() []platform.Field {
	return []platform.Field{
		{Name: "property_id", Label: "Property ID", Help: "GA4 → Admin → Property details: a number such as 123456789"},
		{Name: "service_account_key", Label: "Service account key (JSON)", Secret: true,
			Help: "Google Cloud → IAM → Service accounts → Keys → Add key → JSON, from a project with the Google Analytics Data API enabled. " +
				"Then add the account's email as a Viewer in GA4 → Admin → Property access management"},
	}
}

// key is the part of a service account's JSON key that signing needs.
type key struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
}

func parseKey(raw string) (key, *rsa.PrivateKey, error) {
	invalid := func(msg string) error {
		return &platform.Error{Kind: platform.Rejected, Code: "key_invalid", Msg: msg}
	}
	var k key
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return key{}, nil, invalid("the service account key is not the JSON file Google gives")
	}
	if k.Type != "service_account" || k.ClientEmail == "" {
		return key{}, nil, invalid(`the key is not a service account's ("type": "service_account")`)
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return key{}, nil, invalid("the key's private_key is missing or damaged")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	rk, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok {
		return key{}, nil, invalid("the key's private_key is not an RSA key")
	}
	return k, rk, nil
}

// token signs in as the service account (RFC 7523). A key Google refuses
// was deleted or disabled: AuthRevoked, so the source asks for a new one.
func (s *Source) token(ctx context.Context, c platform.Credentials) (string, error) {
	k, rk, err := parseKey(c["service_account_key"])
	if err != nil {
		return "", err
	}
	now := s.Now()
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signed := seg(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.PrivateKeyID}) + "." +
		seg(map[string]any{"iss": k.ClientEmail, "scope": scope, "aud": TokenURL, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, sum[:])
	if err != nil {
		return "", &platform.Error{Kind: platform.Rejected, Code: "key_invalid", Err: err}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion": {signed + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	t, err := platform.RequestToken(ctx, s.Client, s.TokenURL, form, nil)
	var pe *platform.Error
	if errors.As(err, &pe) && pe.Kind == platform.Rejected && pe.Code == "rejected" {
		pe.Kind, pe.Code = platform.AuthRevoked, "key_refused"
	}
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

type name struct {
	Name string `json:"name"`
}

type dateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

type request struct {
	DateRanges      []dateRange `json:"dateRanges"`
	Dimensions      []name      `json:"dimensions,omitempty"`
	Metrics         []name      `json:"metrics"`
	DimensionFilter any         `json:"dimensionFilter,omitempty"`
	Limit           int         `json:"limit"`
	Offset          int         `json:"offset,omitempty"`
}

type value struct {
	Value string `json:"value"`
}

type response struct {
	Rows []struct {
		DimensionValues []value `json:"dimensionValues"`
		MetricValues    []value `json:"metricValues"`
	} `json:"rows"`
	RowCount int `json:"rowCount"`
	Metadata struct {
		TimeZone string `json:"timeZone"`
	} `json:"metadata"`
}

func names(n ...string) []name {
	out := make([]name, len(n))
	for i, v := range n {
		out[i] = name{v}
	}
	return out
}

// run sends a report request and returns every page of it. Google answers
// 403 when the service account is not on the property (any more).
func (s *Source) run(ctx context.Context, c platform.Credentials, tok string, q request) (*response, error) {
	prop := strings.TrimSpace(c["property_id"])
	headers := map[string]string{"Authorization": "Bearer " + tok}
	all := &response{}
	for q.Limit, q.Offset = s.PageSize, 0; ; q.Offset += s.PageSize {
		var out response
		err := platform.JSON(ctx, s.Client, http.MethodPost, s.APIURL+"/v1beta/properties/"+prop+":runReport", headers, q, &out)
		var pe *platform.Error
		if errors.As(err, &pe) && strings.HasPrefix(pe.Msg, "HTTP 403") {
			pe.Code, pe.Msg = "no_access", "the service account cannot read property "+prop+
				": add its email as a Viewer in GA4 → Admin → Property access management, and enable the Google Analytics Data API in its project"
		}
		if err != nil {
			return nil, err
		}
		all.Rows, all.RowCount, all.Metadata = append(all.Rows, out.Rows...), out.RowCount, out.Metadata
		if len(out.Rows) < s.PageSize || q.Offset+len(out.Rows) >= out.RowCount {
			return all, nil
		}
	}
}

func (s *Source) Verify(ctx context.Context, c platform.Credentials) (analytics.Site, error) {
	prop := strings.TrimSpace(c["property_id"])
	if _, err := strconv.ParseUint(prop, 10, 64); err != nil {
		return analytics.Site{}, &platform.Error{Kind: platform.Rejected, Code: "property_invalid",
			Msg: "the property ID is the number in GA4 → Admin → Property details, not a G- measurement ID"}
	}
	tok, err := s.token(ctx, c)
	if err != nil {
		return analytics.Site{}, err
	}
	out, err := s.run(ctx, c, tok, request{DateRanges: []dateRange{{"today", "today"}}, Metrics: names("totalUsers")})
	if err != nil {
		return analytics.Site{}, err
	}
	return analytics.Site{ID: prop, Name: "GA4 property " + prop, Timezone: out.Metadata.TimeZone}, nil
}

// tagDims are the session's UTM tags: the visit's, as Plausible's are.
var tagDims = []string{"sessionSource", "sessionMedium", "sessionCampaignName", "sessionManualAdContent"}

func (s *Source) Report(ctx context.Context, c platform.Credentials, from, to time.Time, goals []string) ([]analytics.Row, error) {
	tok, err := s.token(ctx, c)
	if err != nil {
		return nil, err
	}
	window := []dateRange{{from.Format(time.DateOnly), to.Format(time.DateOnly)}}
	traffic, err := s.run(ctx, c, tok, request{DateRanges: window, Dimensions: names(append([]string{"date"}, tagDims...)...),
		Metrics: names("totalUsers", "sessions")})
	if err == nil && len(traffic.Rows) > 0 && len(traffic.Rows[0].DimensionValues) != 5 {
		err = decodeErr("unexpected dimensions")
	}
	if err != nil {
		return nil, revoked(err)
	}
	var out []analytics.Row
	for _, r := range traffic.Rows {
		row, err := tagged(r.DimensionValues[0].Value, "", r.DimensionValues[1:])
		if err != nil {
			return nil, err
		}
		row.Visitors, row.Visits = num(r.MetricValues, 0), num(r.MetricValues, 1)
		out = append(out, row)
	}
	if len(goals) == 0 {
		return out, nil
	}
	conv, err := s.run(ctx, c, tok, request{DateRanges: window, Dimensions: names(append([]string{"date", "eventName"}, tagDims...)...),
		Metrics:         names("totalUsers", "eventCount"),
		DimensionFilter: map[string]any{"filter": map[string]any{"fieldName": "eventName", "inListFilter": map[string]any{"values": goals}}}})
	if err == nil && len(conv.Rows) > 0 && len(conv.Rows[0].DimensionValues) != 6 {
		err = decodeErr("unexpected dimensions")
	}
	if err != nil {
		return nil, revoked(err)
	}
	for _, r := range conv.Rows {
		row, err := tagged(r.DimensionValues[0].Value, r.DimensionValues[1].Value, r.DimensionValues[2:])
		if err != nil {
			return nil, err
		}
		row.Visitors, row.Events = num(r.MetricValues, 0), num(r.MetricValues, 1)
		out = append(out, row)
	}
	return out, nil
}

// revoked makes losing access to the property, once connected,
// AuthRevoked: the source asks to be connected again.
func revoked(err error) error {
	var pe *platform.Error
	if errors.As(err, &pe) && pe.Code == "no_access" {
		pe.Kind = platform.AuthRevoked
	}
	return err
}

func decodeErr(what string) error {
	return &platform.Error{Kind: platform.Rejected, Code: "decode", Msg: "Google Analytics answered with " + what}
}

// tagged reads a row's day (GA4 writes 20261001) and session tags.
func tagged(day, goal string, tags []value) (analytics.Row, error) {
	d, err := time.Parse("20060102", day)
	if err != nil {
		return analytics.Row{}, decodeErr("an unreadable day: " + day)
	}
	return analytics.Row{Day: d, Goal: goal, UTM: analytics.UTM{Source: analytics.CleanTag(tags[0].Value), Medium: analytics.CleanTag(tags[1].Value),
		Campaign: analytics.CleanTag(tags[2].Value), Content: analytics.CleanTag(tags[3].Value)}}, nil
}

func num(m []value, i int) int64 {
	if i >= len(m) {
		return 0
	}
	f, _ := strconv.ParseFloat(m[i].Value, 64)
	return int64(f + 0.5)
}
