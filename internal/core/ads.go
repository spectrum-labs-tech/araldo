// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Ad accounts and their results (ADR 0023). This phase reads; nothing
// here spends.
const (
	// AdsReadEvery is how often an account's results are read.
	AdsReadEvery = 24 * time.Hour
	// AdsLookbackDays are re-read on every read, since networks revise a
	// day's numbers as conversions are attributed late.
	AdsLookbackDays = 7
	// AdsBackfillDays are read the first time.
	AdsBackfillDays = 30
	// MaxAdsWindowDays bounds a summary.
	MaxAdsWindowDays = 366
	adsReadLease     = 15 * time.Minute
	adsRetry         = time.Hour
)

func adCredentialsAAD(accountID uuid.UUID) string {
	return keyring.AAD("ad_accounts", "credentials", accountID)
}

// AdNetworkInfo describes an ad network for connect forms.
type AdNetworkInfo struct {
	Network    ads.Network
	Name       string
	Fields     []platform.Field
	Reporting  bool
	Promotions bool
	// OAuth networks connect with a sign-in through a developer app,
	// registered under Provider.
	OAuth    bool
	Provider platform.Provider
}

// AdNetworks lists the networks accounts can connect to in a mode: the
// sandbox in test mode, the install's real networks in live mode.
func (s *Service) AdNetworks(livemode bool) []AdNetworkInfo {
	var out []AdNetworkInfo
	for _, n := range s.adNetworks.Networks() {
		if (n == ads.Sandbox) == livemode {
			continue
		}
		r, _ := s.adNetworks.Get(n)
		_, oauth := r.(platform.Connector)
		out = append(out, AdNetworkInfo{Network: n, Name: r.Name(), Fields: r.Fields(), Reporting: true, OAuth: oauth, Provider: ads.Provider(n)})
	}
	return out
}

// AdAccountInput connects an ad account.
type AdAccountInput struct {
	BrandID uuid.UUID
	Network ads.Network
	Fields  map[string]string
}

// ConnectAdAccount checks credentials with the network and stores the
// account it reaches, to be read soon.
func (s *Service) ConnectAdAccount(ctx context.Context, a Actor, in AdAccountInput) (*model.AdAccount, error) {
	if err := a.require(PermAdsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	switch {
	case !a.Livemode && in.Network != ads.Sandbox:
		return nil, apperr.Invalid("livemode_required", "network", "Test mode can only connect sandbox ad accounts.")
	case a.Livemode && in.Network == ads.Sandbox:
		return nil, apperr.Invalid("testmode_required", "network", "Sandbox ad accounts exist only in test mode.")
	}
	rep, ok := s.adNetworks.Get(in.Network)
	if !ok {
		return nil, apperr.Invalid("network_unsupported", "network", "This install cannot read %q ad accounts.", in.Network)
	}
	settings, secrets, err := splitFields(rep.Name()+" ad accounts", rep.Fields(), in.Fields)
	if err != nil {
		return nil, err
	}
	creds := platform.Credentials{}
	for k, v := range settings {
		creds[k] = v
	}
	for k, v := range secrets {
		creds[k] = v
	}
	acct, err := rep.Verify(ctx, platform.App{}, creds)
	if err != nil {
		return nil, connectError(platform.Provider(rep.Name()), err)
	}
	ac := &model.AdAccount{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, Network: string(in.Network),
		ExternalID: acct.ExternalID, Name: acct.Name, Currency: acct.Currency, Timezone: acct.Timezone, Settings: settings,
		Status: model.AdAccountActive}
	if len(secrets) > 0 {
		raw, err := json.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		if ac.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, adCredentialsAAD(ac.ID), raw); err != nil {
			return nil, err
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateAdAccount(ctx, ac); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("ad_account_connected", "%s is already connected to this brand.", acct.Name)
			}
			return err
		}
		return s.audit(ctx, tx, a, "ad_account.connect", id.Format(id.AdAccount, ac.ID), map[string]any{"network": in.Network})
	})
	return ac, err
}

// AdAccounts lists the actor's ad accounts, optionally for one brand.
func (s *Service) AdAccounts(ctx context.Context, a Actor, brandID *uuid.UUID) ([]*model.AdAccount, error) {
	if err := a.require(PermAdsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if brandID != nil && *brandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		brandID = a.BrandID
	}
	return s.store.AdAccounts(ctx, a.OrgID, a.Livemode, brandID)
}

// AdAccount returns one of the actor's ad accounts.
func (s *Service) AdAccount(ctx context.Context, a Actor, accountID uuid.UUID) (*model.AdAccount, error) {
	if err := a.require(PermAdsRead); err != nil {
		return nil, err
	}
	ac, err := s.store.AdAccount(ctx, a.OrgID, accountID)
	if err != nil {
		return nil, notFound(err, "ad account")
	}
	if ac.Livemode != a.Livemode || a.brandAllowed(ac.BrandID) != nil {
		return nil, apperr.NotFound("ad account")
	}
	return ac, nil
}

// DeleteAdAccount forgets an ad account and its results.
func (s *Service) DeleteAdAccount(ctx context.Context, a Actor, accountID uuid.UUID) error {
	if err := a.require(PermAdsWrite); err != nil {
		return err
	}
	ac, err := s.AdAccount(ctx, a, accountID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteAdAccount(ctx, a.OrgID, ac.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "ad_account.delete", id.Format(id.AdAccount, ac.ID), map[string]any{"network": ac.Network})
	})
}

// AdsFilter narrows an ads summary.
type AdsFilter = store.AdsFilter

// AdsSummary is results added up by group over days Since through Until.
type AdsSummary struct {
	GroupBy      store.AdsGroup
	Since, Until time.Time
	Rows         []store.AdsRow
}

// AdsSummary adds up the actor's ad results. The window defaults to the 30
// days ending today (UTC).
func (s *Service) AdsSummary(ctx context.Context, a Actor, f AdsFilter) (*AdsSummary, error) {
	if err := a.require(PermAdsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if f.BrandID != nil && *f.BrandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		f.BrandID = a.BrandID
	}
	switch f.GroupBy {
	case "":
		f.GroupBy = store.AdsByCampaign
	case store.AdsByBrand, store.AdsByAccount, store.AdsByCampaign, store.AdsByDay:
	default:
		return nil, apperr.Invalid("group_by_invalid", "group_by", "Group by brand, account, campaign or day.")
	}
	if f.Until.IsZero() {
		f.Until = ads.Date(s.Now(), time.UTC)
	}
	if f.Since.IsZero() {
		f.Since = f.Until.AddDate(0, 0, -29)
	}
	if f.Since.After(f.Until) || f.Until.Sub(f.Since) > MaxAdsWindowDays*24*time.Hour {
		return nil, apperr.Invalid("window_invalid", "since", "since must be on or before until, at most a year apart.")
	}
	if f.Limit <= 0 {
		f.Limit = 20
	}
	rows, err := s.store.AdsSummary(ctx, a.OrgID, a.Livemode, f)
	if err != nil {
		return nil, err
	}
	return &AdsSummary{GroupBy: f.GroupBy, Since: f.Since, Until: f.Until, Rows: rows}, nil
}

// CollectAds reads the results of ad accounts that are due.
func (s *Service) CollectAds(ctx context.Context) (int, error) {
	return s.collectAds(ctx, nil)
}

func (s *Service) collectAds(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	due, err := s.store.ClaimDueAdAccounts(ctx, orgID, now, adsReadLease, 20)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ac := range due {
		if err := s.readAdAccount(ctx, ac, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// readAdAccount reads one account's recent days. A failure is recorded on
// the account and retried later, never returned: one account must not
// stop the others.
func (s *Service) readAdAccount(ctx context.Context, ac *model.AdAccount, now time.Time) error {
	rep, ok := s.adNetworks.Get(ads.Network(ac.Network))
	if !ok {
		return s.store.SetAdAccountStatus(ctx, ac.OrgID, ac.ID, ac.Status, fmt.Sprintf("This install cannot read %s ad accounts.", ac.Network),
			now.Add(AdsReadEvery))
	}
	creds, err := s.adCredentials(ctx, ac)
	if err != nil {
		return s.store.SetAdAccountStatus(ctx, ac.OrgID, ac.ID, ac.Status, "Could not decrypt the account's credentials.", now.Add(adsRetry))
	}
	var app platform.App
	if ac.AppID != nil {
		pa, err := s.store.ProviderApp(ctx, ac.OrgID, *ac.AppID)
		if err == nil {
			app, err = s.appCredentials(ctx, pa)
		}
		if err != nil {
			return s.store.SetAdAccountStatus(ctx, ac.OrgID, ac.ID, ac.Status, "Could not read the developer app it connected through.", now.Add(adsRetry))
		}
	}
	loc := location(ac.Timezone)
	to := ads.Date(now, loc)
	days := AdsLookbackDays
	if ac.ReadAt == nil {
		days = AdsBackfillDays
	}
	results, err := rep.Report(ctx, app, creds, to.AddDate(0, 0, -(days-1)), to)
	var pe *platform.Error
	switch {
	case err == nil:
		return s.store.SaveAdResults(ctx, ac.OrgID, ac.ID, results, now, now.Add(AdsReadEvery))
	case errors.As(err, &pe) && pe.Kind == platform.AuthRevoked:
		return s.store.SetAdAccountStatus(ctx, ac.OrgID, ac.ID, model.AdAccountNeedsReauth,
			truncate("The network refused the credentials: connect the account again. "+pe.Msg, 500), now.Add(AdsReadEvery))
	default:
		s.log.WarnContext(ctx, "reading an ad account failed; will retry", "ad_account", id.Format(id.AdAccount, ac.ID), "err", err)
		return s.store.SetAdAccountStatus(ctx, ac.OrgID, ac.ID, ac.Status, truncate("Reading results failed; retrying: "+err.Error(), 500),
			now.Add(adsRetry))
	}
}

// connectAdAccounts turns a sign-in's connections into ad accounts of its
// brand, reconnecting any the brand already has. The network describes each
// account (name, currency, time zone) with the new credentials.
func (s *Service) connectAdAccounts(ctx context.Context, a Actor, st *model.OAuthState, network ads.Network, conns []platform.Connection) ([]*model.AdAccount, error) {
	rep, ok := s.adNetworks.Get(network)
	if !ok {
		return nil, apperr.Invalid("network_unsupported", "provider", "This install cannot read %q ad accounts.", network)
	}
	app, err := s.store.ProviderApp(ctx, a.OrgID, st.AppID)
	if err != nil {
		return nil, notFound(err, "app")
	}
	appCreds, err := s.appCredentials(ctx, app)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.AdAccounts(ctx, a.OrgID, true, &st.BrandID)
	if err != nil {
		return nil, err
	}
	var out []*model.AdAccount
	for _, c := range conns {
		acct, err := rep.Verify(ctx, appCreds, c.Credentials)
		if err != nil {
			return nil, connectError(platform.Provider(rep.Name()), err)
		}
		ac := &model.AdAccount{ID: id.New(), OrgID: a.OrgID, BrandID: st.BrandID, Livemode: true, Network: string(network),
			ExternalID: acct.ExternalID, Settings: map[string]string{}, Status: model.AdAccountActive}
		reconnect := false
		for _, e := range existing {
			if e.Network == string(network) && e.ExternalID == acct.ExternalID {
				ac, reconnect = e, true
			}
		}
		ac.Name, ac.Currency, ac.Timezone, ac.AppID = acct.Name, acct.Currency, acct.Timezone, &st.AppID
		raw, err := json.Marshal(c.Credentials)
		if err != nil {
			return nil, err
		}
		if ac.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, adCredentialsAAD(ac.ID), raw); err != nil {
			return nil, err
		}
		action := "ad_account.connect"
		err = s.store.InTx(ctx, func(tx *store.Store) error {
			if reconnect {
				action = "ad_account.reconnect"
				if err := tx.UpdateAdAccountConnection(ctx, ac); err != nil {
					return err
				}
			} else if err := tx.CreateAdAccount(ctx, ac); err != nil {
				return err
			}
			return s.audit(ctx, tx, a, action, id.Format(id.AdAccount, ac.ID), map[string]any{"network": network})
		})
		if err != nil {
			return nil, err
		}
		out = append(out, ac)
	}
	return out, nil
}

func (s *Service) adCredentials(ctx context.Context, ac *model.AdAccount) (platform.Credentials, error) {
	creds := platform.Credentials{}
	for k, v := range ac.Settings {
		creds[k] = v
	}
	if len(ac.Credentials) == 0 {
		return creds, nil
	}
	raw, err := s.keys.Decrypt(ctx, ac.OrgID, adCredentialsAAD(ac.ID), ac.Credentials)
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, err
	}
	for k, v := range secrets {
		creds[k] = v
	}
	return creds, nil
}

// AdNetworkView is an ad network in the API.
type AdNetworkView struct {
	Network       string           `json:"network"`
	Name          string           `json:"name"`
	Reporting     bool             `json:"reporting"`
	Promotions    bool             `json:"promotions"`
	ConnectFields []platform.Field `json:"connect_fields"`
}

// ViewAdNetwork renders an ad network.
func ViewAdNetwork(n AdNetworkInfo) AdNetworkView {
	fields := n.Fields
	if fields == nil {
		fields = []platform.Field{}
	}
	return AdNetworkView{Network: string(n.Network), Name: n.Name, Reporting: n.Reporting, Promotions: n.Promotions, ConnectFields: fields}
}

// AdAccountView is an ad account in the API. Credentials never appear.
type AdAccountView struct {
	ID         string     `json:"id"`
	Object     string     `json:"object"`
	Brand      string     `json:"brand"`
	Livemode   bool       `json:"livemode"`
	Network    string     `json:"network"`
	ExternalID string     `json:"external_id"`
	Name       string     `json:"name"`
	Currency   string     `json:"currency"`
	Timezone   string     `json:"timezone"`
	Status     string     `json:"status"`
	StatusNote string     `json:"status_note,omitempty"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ViewAdAccount renders an ad account.
func ViewAdAccount(a *model.AdAccount) AdAccountView {
	return AdAccountView{ID: id.Format(id.AdAccount, a.ID), Object: "ad_account", Brand: id.Format(id.Brand, a.BrandID), Livemode: a.Livemode,
		Network: a.Network, ExternalID: a.ExternalID, Name: a.Name, Currency: a.Currency, Timezone: a.Timezone, Status: string(a.Status),
		StatusNote: a.StatusNote, ReadAt: utc(a.ReadAt), CreatedAt: a.CreatedAt.UTC()}
}

// AdsRowView is one group of an ads summary in the API.
type AdsRowView struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Network      string `json:"network,omitempty"`
	Currency     string `json:"currency"`
	Spend        int64  `json:"spend"`
	Impressions  int64  `json:"impressions"`
	Clicks       int64  `json:"clicks"`
	Results      int64  `json:"results"`
	CostPerClick int64  `json:"cost_per_click"`
}

// AdsSummaryView is an ads summary in the API.
type AdsSummaryView struct {
	Object  string       `json:"object"`
	GroupBy string       `json:"group_by"`
	Since   string       `json:"since"`
	Until   string       `json:"until"`
	Data    []AdsRowView `json:"data"`
}

// ViewAdsSummary renders an ads summary.
func ViewAdsSummary(sum *AdsSummary) AdsSummaryView {
	v := AdsSummaryView{Object: "ads_summary", GroupBy: string(sum.GroupBy), Since: sum.Since.Format(time.DateOnly),
		Until: sum.Until.Format(time.DateOnly), Data: make([]AdsRowView, 0, len(sum.Rows))}
	for _, r := range sum.Rows {
		v.Data = append(v.Data, AdsRowView{ID: AdsRowID(sum.GroupBy, r.Key), Label: r.Label, Network: r.Network, Currency: r.Currency,
			Spend: r.Spend, Impressions: r.Impressions, Clicks: r.Clicks, Results: r.Results, CostPerClick: r.CostPerClick()})
	}
	return v
}

// AdsRowID formats a summary row's ID for its grouping: brands and accounts
// get Araldo IDs, campaigns keep the network's and days are dates.
func AdsRowID(group store.AdsGroup, key string) string {
	switch group {
	case store.AdsByBrand, store.AdsByAccount:
		u, err := uuid.Parse(key)
		if err != nil {
			return key
		}
		if group == store.AdsByBrand {
			return id.Format(id.Brand, u)
		}
		return id.Format(id.AdAccount, u)
	}
	return key
}
