// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plausible reads Plausible Analytics (plausible.io, or a
// self-hosted Community Edition) through its Stats API v2
// (https://plausible.io/docs/stats-api): visitors and visits, and each
// goal's conversions, by day and UTM tags. It reads counts only.
package plausible

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// DefaultURL is Plausible's hosted service.
const DefaultURL = "https://plausible.io"

// Source reads Plausible.
type Source struct {
	Client *http.Client
	// PageSize is the rows asked for per request; tests make it small.
	PageSize int
}

// New returns the Plausible source.
func New(client *http.Client) *Source { return &Source{Client: client, PageSize: 10000} }

func (s *Source) Provider() analytics.Provider { return "plausible" }
func (s *Source) Name() string                 { return "Plausible" }

func (s *Source) Fields() []platform.Field {
	return []platform.Field{
		{Name: "site_id", Label: "Site", Help: "The site's domain as Plausible lists it, e.g. example.com"},
		{Name: "api_key", Label: "Stats API key", Secret: true, Help: "Plausible → Account settings → API keys → New API key → Stats API"},
		{Name: "base_url", Label: "Plausible URL", Optional: true, Default: DefaultURL,
			Help: "Your own install's address if you self-host Plausible"},
	}
}

func base(c platform.Credentials) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(c["base_url"]), "/")
	if b == "" {
		b = DefaultURL
	}
	u, err := url.Parse(b)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", &platform.Error{Kind: platform.Rejected, Code: "base_url_invalid", Msg: "the Plausible URL must start with https://"}
	}
	return b, nil
}

type query struct {
	SiteID     string     `json:"site_id"`
	Metrics    []string   `json:"metrics"`
	DateRange  any        `json:"date_range"`
	Dimensions []string   `json:"dimensions,omitempty"`
	Filters    []any      `json:"filters,omitempty"`
	Pagination pagination `json:"pagination"`
}

type pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type result struct {
	Dimensions []json.RawMessage `json:"dimensions"`
	Metrics    []json.Number     `json:"metrics"`
}

// run sends a query and returns every page of results.
func (s *Source) run(ctx context.Context, c platform.Credentials, q query) ([]result, error) {
	b, err := base(c)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Authorization": "Bearer " + c["api_key"]}
	var all []result
	for q.Pagination = (pagination{Limit: s.PageSize}); ; q.Pagination.Offset += s.PageSize {
		var out struct {
			Results []result `json:"results"`
		}
		if err := platform.JSON(ctx, s.Client, http.MethodPost, b+"/api/v2/query", headers, q, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Results...)
		if len(out.Results) < s.PageSize {
			return all, nil
		}
	}
}

func (s *Source) Verify(ctx context.Context, c platform.Credentials) (analytics.Site, error) {
	site := strings.TrimSpace(c["site_id"])
	if site == "" || c["api_key"] == "" {
		return analytics.Site{}, &platform.Error{Kind: platform.Rejected, Code: "fields_missing", Msg: "the site and an API key are required"}
	}
	if _, err := s.run(ctx, c, query{SiteID: site, Metrics: []string{"visitors"}, DateRange: "day"}); err != nil {
		return analytics.Site{}, err
	}
	// The Stats API counts days in the site's own time zone, which it does
	// not report; the dates are taken as they come.
	return analytics.Site{ID: site, Name: site}, nil
}

var tagDims = []string{"visit:utm_source", "visit:utm_medium", "visit:utm_campaign", "visit:utm_content"}

func (s *Source) Report(ctx context.Context, c platform.Credentials, from, to time.Time, goals []string) ([]analytics.Row, error) {
	site := strings.TrimSpace(c["site_id"])
	window := []string{from.Format(time.DateOnly), to.Format(time.DateOnly)}
	traffic, err := s.run(ctx, c, query{SiteID: site, Metrics: []string{"visitors", "visits"}, DateRange: window,
		Dimensions: append([]string{"time:day"}, tagDims...)})
	if err != nil {
		return nil, err
	}
	var out []analytics.Row
	for _, r := range traffic {
		row, err := tagged(r, false)
		if err != nil {
			return nil, err
		}
		row.Visitors, row.Visits = num(r.Metrics, 0), num(r.Metrics, 1)
		out = append(out, row)
	}
	if len(goals) == 0 {
		return out, nil
	}
	conv, err := s.run(ctx, c, query{SiteID: site, Metrics: []string{"visitors", "events"}, DateRange: window,
		Dimensions: append([]string{"time:day", "event:goal"}, tagDims...), Filters: []any{[]any{"is", "event:goal", goals}}})
	if err != nil {
		return nil, err
	}
	for _, r := range conv {
		row, err := tagged(r, true)
		if err != nil {
			return nil, err
		}
		row.Visitors, row.Events = num(r.Metrics, 0), num(r.Metrics, 1)
		out = append(out, row)
	}
	return out, nil
}

// tagged reads a result's dimensions: the day, the goal when withGoal,
// then the four UTM tags.
func tagged(r result, withGoal bool) (analytics.Row, error) {
	dims := make([]string, len(r.Dimensions))
	for i, d := range r.Dimensions {
		var v string
		if json.Unmarshal(d, &v) != nil {
			v = "" // a null tag
		}
		dims[i] = v
	}
	want := 5
	if withGoal {
		want = 6
	}
	if len(dims) != want {
		return analytics.Row{}, &platform.Error{Kind: platform.Rejected, Code: "decode", Msg: "Plausible answered with unexpected dimensions"}
	}
	day, err := time.Parse(time.DateOnly, dims[0][:min(len(dims[0]), len(time.DateOnly))])
	if err != nil {
		return analytics.Row{}, &platform.Error{Kind: platform.Rejected, Code: "decode", Msg: "Plausible answered with an unreadable day: " + dims[0]}
	}
	row := analytics.Row{Day: day}
	tags := dims[1:]
	if withGoal {
		row.Goal, tags = dims[1], dims[2:]
	}
	row.UTM = analytics.UTM{Source: analytics.CleanTag(tags[0]), Medium: analytics.CleanTag(tags[1]),
		Campaign: analytics.CleanTag(tags[2]), Content: analytics.CleanTag(tags[3])}
	return row, nil
}

func num(m []json.Number, i int) int64 {
	if i >= len(m) {
		return 0
	}
	f, _ := m[i].Float64()
	return int64(f + 0.5)
}
