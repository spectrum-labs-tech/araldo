// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Ad accounts and their results (ADR 0023).

const adAccountCols = `id, org_id, brand_id, livemode, network, external_id, name, currency, timezone, settings, credentials,
	status, status_note, read_at, next_read_at, created_at`

func scanAdAccount(r pgx.Row) (*model.AdAccount, error) {
	var a model.AdAccount
	err := r.Scan(&a.ID, &a.OrgID, &a.BrandID, &a.Livemode, &a.Network, &a.ExternalID, &a.Name, &a.Currency, &a.Timezone, &a.Settings,
		&a.Credentials, &a.Status, &a.StatusNote, &a.ReadAt, &a.NextReadAt, &a.CreatedAt)
	return &a, mapErr(err)
}

func collectAdAccounts(rows pgx.Rows, err error) ([]*model.AdAccount, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.AdAccount, error) { return scanAdAccount(r) })
}

// CreateAdAccount stores a new ad account, due to be read at once. The
// same network account on the same brand and mode is ErrConflict.
func (s *Store) CreateAdAccount(ctx context.Context, a *model.AdAccount) error {
	if a.Settings == nil {
		a.Settings = map[string]string{}
	}
	return mapErr(s.q.QueryRow(ctx, `INSERT INTO ad_accounts (id, org_id, brand_id, livemode, network, external_id, name, currency, timezone,
		settings, credentials, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING next_read_at, created_at`,
		a.ID, a.OrgID, a.BrandID, a.Livemode, a.Network, a.ExternalID, a.Name, a.Currency, a.Timezone, a.Settings, a.Credentials, a.Status).
		Scan(&a.NextReadAt, &a.CreatedAt))
}

// AdAccount returns one of an org's ad accounts.
func (s *Store) AdAccount(ctx context.Context, orgID, id uuid.UUID) (*model.AdAccount, error) {
	return scanAdAccount(s.q.QueryRow(ctx, `SELECT `+adAccountCols+` FROM ad_accounts WHERE org_id = $1 AND id = $2`, orgID, id))
}

// AdAccounts lists an org's ad accounts in a mode, optionally for one brand.
func (s *Store) AdAccounts(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID) ([]*model.AdAccount, error) {
	return collectAdAccounts(s.q.Query(ctx, `SELECT `+adAccountCols+` FROM ad_accounts WHERE org_id = $1 AND livemode = $2
		AND ($3::uuid IS NULL OR brand_id = $3) ORDER BY brand_id, network, name, id`, orgID, livemode, brandID))
}

// DeleteAdAccount forgets an ad account and its results.
func (s *Store) DeleteAdAccount(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM ad_accounts WHERE org_id = $1 AND id = $2`, orgID, id)
}

// ClaimDueAdAccounts takes active accounts due for reading, in one org or
// (orgID nil) every org, and pushes their next read back by lease so no
// other worker takes them meanwhile.
func (s *Store) ClaimDueAdAccounts(ctx context.Context, orgID *uuid.UUID, now time.Time, lease time.Duration, limit int) ([]*model.AdAccount, error) {
	return collectAdAccounts(s.q.Query(ctx, `UPDATE ad_accounts SET next_read_at = $3 WHERE id IN (
			SELECT id FROM ad_accounts WHERE status = 'active' AND next_read_at <= $2 AND ($1::uuid IS NULL OR org_id = $1)
			ORDER BY next_read_at LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING `+adAccountCols, orgID, now, now.Add(lease), limit))
}

// SaveAdResults replaces an account's results for the days they cover and
// schedules its next read.
func (s *Store) SaveAdResults(ctx context.Context, orgID, accountID uuid.UUID, results []ads.Result, readAt, next time.Time) error {
	return s.InTx(ctx, func(tx *Store) error {
		for _, r := range results {
			if _, err := tx.q.Exec(ctx, `INSERT INTO ad_results (org_id, ad_account_id, campaign_id, campaign_name, day, spend, impressions, clicks, results, read_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (ad_account_id, campaign_id, day) DO UPDATE SET campaign_name = EXCLUDED.campaign_name, spend = EXCLUDED.spend,
					impressions = EXCLUDED.impressions, clicks = EXCLUDED.clicks, results = EXCLUDED.results, read_at = EXCLUDED.read_at`,
				orgID, accountID, r.CampaignID, r.CampaignName, r.Day, r.Spend, r.Impressions, r.Clicks, r.Results, readAt); err != nil {
				return mapErr(err)
			}
		}
		return tx.execOne(ctx, `UPDATE ad_accounts SET read_at = $3, next_read_at = $4, status_note = '', updated_at = now()
			WHERE org_id = $1 AND id = $2`, orgID, accountID, readAt, next)
	})
}

// SetAdAccountStatus records why an account could not be read, and when to
// try again.
func (s *Store) SetAdAccountStatus(ctx context.Context, orgID, id uuid.UUID, status model.AdAccountStatus, note string, next time.Time) error {
	return s.execOne(ctx, `UPDATE ad_accounts SET status = $3, status_note = $4, next_read_at = $5, updated_at = now() WHERE org_id = $1 AND id = $2`,
		orgID, id, status, note, next)
}

// AdsGroup is how an ads summary groups results.
type AdsGroup string

// Groupings.
const (
	AdsByBrand    AdsGroup = "brand"
	AdsByAccount  AdsGroup = "account"
	AdsByCampaign AdsGroup = "campaign"
	AdsByDay      AdsGroup = "day"
)

// AdsFilter narrows an ads summary to days Since through Until.
type AdsFilter struct {
	GroupBy      AdsGroup
	BrandID      *uuid.UUID
	AccountID    *uuid.UUID
	Since, Until time.Time
	Limit        int
}

// AdsRow is one group of an ads summary.
type AdsRow struct {
	// Key is the brand or account ID, the campaign's network ID, or the day
	// (YYYY-MM-DD).
	Key       string
	Label     string
	Network   string
	Currency  string
	AccountID *uuid.UUID // for campaigns
	ads.Totals
}

// AdsSummary adds up results by group, per currency.
func (s *Store) AdsSummary(ctx context.Context, orgID uuid.UUID, livemode bool, f AdsFilter) ([]AdsRow, error) {
	var key, label, network, order string
	account := "NULL::uuid"
	switch f.GroupBy {
	case AdsByBrand:
		key, label, network, order = "b.id::text", "b.name", "''", "spend DESC"
	case AdsByAccount:
		key, label, network, order = "a.id::text", "a.name", "a.network", "spend DESC"
	case AdsByCampaign:
		key, label, network, order = "r.campaign_id", "max(r.campaign_name)", "a.network", "spend DESC"
		account = "a.id"
	case AdsByDay:
		key, label, network, order = "to_char(r.day, 'YYYY-MM-DD')", "to_char(r.day, 'YYYY-MM-DD')", "''", "key ASC"
	default:
		return nil, fmt.Errorf("store: unknown ads grouping %q", f.GroupBy)
	}
	group := key + ", a.currency"
	switch f.GroupBy {
	case AdsByBrand:
		group += ", b.name"
	case AdsByAccount:
		group += ", a.name, a.network"
	case AdsByCampaign:
		group += ", a.id, a.network"
	}
	rows, err := s.q.Query(ctx, `SELECT `+key+` AS key, `+label+`, `+network+`, a.currency, `+account+`,
			sum(r.spend) AS spend, sum(r.impressions), sum(r.clicks), sum(r.results)
		FROM ad_results r JOIN ad_accounts a ON a.id = r.ad_account_id JOIN brands b ON b.id = a.brand_id
		WHERE r.org_id = $1 AND a.livemode = $2 AND ($3::uuid IS NULL OR a.brand_id = $3) AND ($4::uuid IS NULL OR a.id = $4)
			AND r.day BETWEEN $5 AND $6
		GROUP BY `+group+` ORDER BY `+order+`, key LIMIT $7`,
		orgID, livemode, f.BrandID, f.AccountID, f.Since, f.Until, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AdsRow, error) {
		var row AdsRow
		err := r.Scan(&row.Key, &row.Label, &row.Network, &row.Currency, &row.AccountID, &row.Spend, &row.Impressions, &row.Clicks, &row.Results)
		return row, err
	})
}
