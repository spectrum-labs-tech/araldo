// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
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
