// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Ads (ADR 0023).

type adsData struct {
	Brands    []*model.Brand
	BrandName map[string]string
	Brand     string
	Days      int
	Accounts  []*model.AdAccount
	Campaigns *core.AdsSummary
	Daily     *core.AdsSummary
	// CanWrite shows the connect form and the disconnect buttons.
	CanWrite bool
	Networks []core.AdNetworkInfo
	Network  *core.AdNetworkInfo
	Values   map[string]string
	// Apps are the org's developer apps for the network's sign-in.
	Apps []*model.ProviderApp
	// For the guide: the redirect URI to register, whether any account has
	// been read, and the link builder's form and result.
	Redirect   string
	HasResults bool
	Tag        tagForm
}

// tagForm is the guide's UTM link builder.
type tagForm struct {
	URL, Source, Campaign, Content string
	Link, Err                      string
}

func (s *Server) adsPage(c *reqCtx) error {
	d, err := s.adsData(c)
	if err != nil {
		return err
	}
	return s.page(c, "ads", "ads", "Ads", d)
}

func (s *Server) adsData(c *reqCtx) (*adsData, error) {
	q := c.r.URL.Query()
	d := &adsData{Days: 30, Brand: q.Get("brand"), BrandName: map[string]string{}, CanWrite: c.actor.Can(core.PermAdsWrite),
		Networks: s.svc.AdNetworks(c.actor.Livemode), Values: map[string]string{}}
	if n, err := strconv.Atoi(q.Get("days")); err == nil && n >= 1 && n <= 365 {
		d.Days = n
	}
	for i := range d.Networks {
		if string(d.Networks[i].Network) == q.Get("network") || d.Network == nil {
			d.Network = &d.Networks[i]
		}
	}
	if d.Network != nil && d.Network.OAuth {
		d.Redirect = s.svc.ConnectRedirectURI(d.Network.Provider)
	}
	d.Tag = tagForm{URL: q.Get("tag_url"), Source: q.Get("tag_source"), Campaign: q.Get("tag_campaign"), Content: q.Get("tag_content")}
	if d.Tag.Source == "" && d.Network != nil && d.Network.Network != ads.Sandbox {
		d.Tag.Source = string(d.Network.Network)
	}
	if q.Has("tag_url") {
		d.Tag.Link, d.Tag.Err = tagLink(d.Tag.URL, d.Tag.Source, d.Tag.Campaign, d.Tag.Content)
	}
	var err error
	if d.Network != nil && d.Network.OAuth && d.CanWrite {
		apps, err := s.svc.ProviderApps(c.ctx(), c.actor)
		if err != nil {
			return nil, err
		}
		for _, a := range apps {
			if a.Provider == d.Network.Provider {
				d.Apps = append(d.Apps, a)
			}
		}
	}
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return nil, err
	}
	for _, b := range d.Brands {
		d.BrandName[id.Format(id.Brand, b.ID)] = b.Name
	}
	f := core.AdsFilter{Until: ads.Date(time.Now(), time.UTC), Limit: 100}
	f.Since = f.Until.AddDate(0, 0, -(d.Days - 1))
	if d.Brand != "" {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, d.Brand)
		if err != nil {
			return nil, err
		}
		f.BrandID = &b.ID
	}
	if d.Accounts, err = s.svc.AdAccounts(c.ctx(), c.actor, f.BrandID); err != nil {
		return nil, err
	}
	for _, a := range d.Accounts {
		d.HasResults = d.HasResults || a.ReadAt != nil
	}
	f.GroupBy = store.AdsByCampaign
	if d.Campaigns, err = s.svc.AdsSummary(c.ctx(), c.actor, f); err != nil {
		return nil, err
	}
	f.GroupBy = store.AdsByDay
	if d.Daily, err = s.svc.AdsSummary(c.ctx(), c.actor, f); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Server) connectAdAccount(c *reqCtx) error {
	f := c.r.PostForm
	d, err := s.adsData(c)
	if err != nil {
		return err
	}
	in := core.AdAccountInput{Network: ads.Network(f.Get("network")), Fields: map[string]string{}}
	for k, v := range f {
		if name, ok := strings.CutPrefix(k, "field_"); ok && len(v) > 0 {
			in.Fields[name] = v[0]
			d.Values[name] = v[0]
		}
	}
	for i := range d.Networks {
		if d.Networks[i].Network == in.Network {
			d.Network = &d.Networks[i]
		}
	}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Get("brand"))
	if err != nil {
		return s.formErr(c, "ads", "ads", "Ads", d, err)
	}
	in.BrandID = b.ID
	ac, err := s.svc.ConnectAdAccount(c.ctx(), c.actor, in)
	if err != nil {
		return s.formErr(c, "ads", "ads", "Ads", d, err)
	}
	return redirect(c, "/ads", "Connected "+ac.Name+". Its results are read within a few minutes, then daily.")
}

func (s *Server) deleteAdAccount(c *reqCtx) error {
	aid, err := pathUUID(c, id.AdAccount, "ad account")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteAdAccount(c.ctx(), c.actor, aid); err != nil {
		return err
	}
	return redirect(c, "/ads", "Disconnected.")
}

// zeroDecimal are ISO 4217 currencies without a minor unit.
var zeroDecimal = map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true}

// money formats an amount in a currency's minor unit, as "12.34 USD".
func money(minor int64, currency string) string {
	if zeroDecimal[currency] {
		return strconv.FormatInt(minor, 10) + " " + currency
	}
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return sign + strconv.FormatInt(minor/100, 10) + "." + leftPad(strconv.FormatInt(minor%100, 10)) + " " + currency
}

func leftPad(s string) string {
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// tagLink adds UTM parameters to a landing page link for an ad, so the
// site's own analytics count what the ad brought (ADR 0023): medium "paid",
// the network as source, and the campaign and ad as cleaned tokens.
func tagLink(raw, source, campaign, content string) (link, problem string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", "Give the landing page's full address, starting with https://."
	}
	if campaign = utmToken(campaign); campaign == "" {
		return "", "Name the campaign."
	}
	q := u.Query()
	q.Set("utm_source", firstNonEmpty(utmToken(source), "ads"))
	q.Set("utm_medium", "paid")
	q.Set("utm_campaign", campaign)
	if content = utmToken(content); content != "" {
		q.Set("utm_content", content)
	} else {
		q.Del("utm_content")
	}
	u.RawQuery = q.Encode()
	return u.String(), ""
}

// utmToken makes a value a short lowercase token ("Alpha launch!" becomes
// "alpha-launch"): what analytics group by, and what sites that keep only
// such tokens accept.
func utmToken(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	return out
}
