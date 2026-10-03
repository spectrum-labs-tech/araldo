// SPDX-License-Identifier: AGPL-3.0-or-later

// Package reddit reads Reddit Ads accounts (ADR 0023) through the Reddit
// Ads API v3. An account connects with a sign-in through the org's Reddit
// app (a "web app" at reddit.com/prefs/apps with Ads API access), asking
// only for adsread and a permanent refresh token; every read swaps that
// for an hour-long access token. Nothing here spends.
package reddit

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI  = "https://ads-api.reddit.com/api/v3"
	DefaultAuth = "https://www.reddit.com/api/v1"
	scopes      = "adsread"
	pageSize    = "1000"
)

// Network reads Reddit Ads.
type Network struct {
	Client *http.Client
	// API and Auth are Reddit's endpoints; tests replace them.
	API, Auth string
}

// New returns the Reddit Ads network.
func New(client *http.Client) *Network {
	return &Network{Client: client, API: DefaultAPI, Auth: DefaultAuth}
}

func (n *Network) Network() ads.Network { return "reddit" }
func (n *Network) Name() string         { return "Reddit" }

// Fields are none: accounts connect with a sign-in.
func (n *Network) Fields() []platform.Field { return nil }

func (n *Network) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	q := url.Values{"client_id": {app.ClientID}, "response_type": {"code"}, "state": {state}, "redirect_uri": {redirectURI},
		"duration": {"permanent"}, "scope": {scopes}}
	return n.Auth + "/authorize?" + q.Encode()
}

// account is an ad account as Reddit returns it.
type account struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Currency   string `json:"currency"`
	TimeZoneID string `json:"time_zone_id"`
}

// Exchange trades the code for a refresh token and lists the ad accounts of
// the member's businesses, to choose from.
func (n *Network) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	t, err := platform.RequestToken(ctx, n.Client, n.Auth+"/access_token", form, &app)
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		return nil, &platform.Error{Kind: platform.Rejected, Code: "refresh_token_missing",
			Msg: "Reddit gave no refresh token; the sign-in must ask for permanent access"}
	}
	var businesses []struct {
		ID string `json:"id"`
	}
	if err := n.list(ctx, t.AccessToken, "/me/businesses", &businesses); err != nil {
		return nil, err
	}
	var out []platform.Connection
	for _, b := range businesses {
		var accounts []account
		if err := n.list(ctx, t.AccessToken, "/businesses/"+url.PathEscape(b.ID)+"/ad_accounts", &accounts); err != nil {
			return nil, err
		}
		for _, a := range accounts {
			out = append(out, platform.Connection{
				Account:     platform.Account{ExternalID: a.ID, Handle: a.Currency, DisplayName: a.Name},
				Credentials: platform.Credentials{"refresh_token": t.RefreshToken, "account_id": a.ID},
			})
		}
	}
	return out, nil
}

// accessToken swaps the refresh token for an access token.
func (n *Network) accessToken(ctx context.Context, app platform.App, c platform.Credentials) (string, error) {
	if c["refresh_token"] == "" || c["account_id"] == "" {
		return "", platform.Errorf(platform.AuthRevoked, "the account has no Reddit token: connect it again")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]}}
	t, err := platform.RequestToken(ctx, n.Client, n.Auth+"/access_token", form, &app)
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

func (n *Network) Verify(ctx context.Context, app platform.App, c platform.Credentials) (ads.Account, error) {
	tok, err := n.accessToken(ctx, app, c)
	if err != nil {
		return ads.Account{}, err
	}
	return n.account(ctx, tok, c["account_id"])
}

func (n *Network) account(ctx context.Context, tok, id string) (ads.Account, error) {
	var out struct {
		Data account `json:"data"`
	}
	if err := platform.JSON(ctx, n.Client, http.MethodGet, n.API+"/ad_accounts/"+url.PathEscape(id), bearer(tok), nil, &out); err != nil {
		return ads.Account{}, err
	}
	a := out.Data
	if a.ID == "" {
		a.ID = id
	}
	tz := a.TimeZoneID
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		tz = "UTC"
	}
	return ads.Account{ExternalID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.Currency), Timezone: tz}, nil
}

// metric is one row of a report, its keys lowercase as Reddit returns them.
// Spend is in millionths of the currency.
type metric struct {
	Date        string      `json:"date"`
	CampaignID  string      `json:"campaign_id"`
	Spend       json.Number `json:"spend"`
	Impressions json.Number `json:"impressions"`
	Clicks      json.Number `json:"clicks"`
}

// Report reads every campaign's spend, impressions and clicks by day, in
// the account's time zone. Results are 0: they count conversions, which
// Reddit measures with its pixel, which Araldo does not ask for (ADR 0023).
func (n *Network) Report(ctx context.Context, app platform.App, c platform.Credentials, from, to time.Time) ([]ads.Result, error) {
	tok, err := n.accessToken(ctx, app, c)
	if err != nil {
		return nil, err
	}
	id := c["account_id"]
	acct, err := n.account(ctx, tok, id)
	if err != nil {
		return nil, err
	}
	var campaigns []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := n.list(ctx, tok, "/ad_accounts/"+url.PathEscape(id)+"/campaigns", &campaigns); err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, cmp := range campaigns {
		names[cmp.ID] = cmp.Name
	}
	const hour = "2006-01-02T15:00:00Z"
	body := map[string]any{"data": map[string]any{
		"starts_at":    from.Format(hour),
		"ends_at":      to.AddDate(0, 0, 1).Format(hour),
		"fields":       []string{"SPEND", "IMPRESSIONS", "CLICKS"},
		"breakdowns":   []string{"DATE", "CAMPAIGN_ID"},
		"time_zone_id": acct.Timezone,
	}}
	var out []ads.Result
	next := n.API + "/ad_accounts/" + url.PathEscape(id) + "/reports?page.size=" + pageSize
	for next != "" {
		var page struct {
			Data struct {
				Metrics []metric `json:"metrics"`
			} `json:"data"`
			Pagination struct {
				NextURL string `json:"next_url"`
			} `json:"pagination"`
		}
		if err := platform.JSON(ctx, n.Client, http.MethodPost, next, bearer(tok), body, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data.Metrics {
			day, err := time.Parse(time.DateOnly, m.Date[:min(len(m.Date), len(time.DateOnly))])
			if err != nil {
				return nil, &platform.Error{Kind: platform.Uncertain, Code: "decode", Msg: "a report row has no readable date: " + m.Date}
			}
			name := names[m.CampaignID]
			if name == "" {
				name = m.CampaignID
			}
			out = append(out, ads.Result{CampaignID: m.CampaignID, CampaignName: name, Day: day, Spend: minorUnits(m.Spend, acct.Currency),
				Impressions: integer(m.Impressions), Clicks: integer(m.Clicks)})
		}
		if next, err = n.sameAPI(page.Pagination.NextURL); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// list reads every page of a GET list endpoint into out (a pointer to a
// slice).
func (n *Network) list(ctx context.Context, tok, path string, out any) error {
	var all []json.RawMessage
	next := n.API + path + "?page.size=" + pageSize
	for next != "" {
		var page struct {
			Data       []json.RawMessage `json:"data"`
			Pagination struct {
				NextURL string `json:"next_url"`
			} `json:"pagination"`
		}
		if err := platform.JSON(ctx, n.Client, http.MethodGet, next, bearer(tok), nil, &page); err != nil {
			return err
		}
		all = append(all, page.Data...)
		var err error
		if next, err = n.sameAPI(page.Pagination.NextURL); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// sameAPI returns a next-page link only if it is on the Ads API, so the
// access token never goes anywhere else.
func (n *Network) sameAPI(next string) (string, error) {
	if next == "" {
		return "", nil
	}
	u, err := url.Parse(next)
	base, _ := url.Parse(n.API)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return "", &platform.Error{Kind: platform.Rejected, Code: "pagination_invalid", Msg: "a next page outside the Ads API: " + next}
	}
	return next, nil
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func integer(n json.Number) int64 {
	f, _ := n.Float64()
	return int64(math.Round(f))
}

// zeroDecimal are ISO 4217 currencies without a minor unit.
var zeroDecimal = map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true}

// minorUnits converts millionths of a currency to its minor unit (cents),
// rounded.
func minorUnits(micros json.Number, currency string) int64 {
	f, _ := micros.Float64()
	if zeroDecimal[currency] {
		return int64(math.Round(f / 1e6))
	}
	return int64(math.Round(f / 1e4))
}
