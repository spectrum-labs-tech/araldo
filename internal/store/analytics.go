// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Web analytics sources and their counts (ADR 0025).

const analyticsSourceCols = `id, org_id, brand_id, livemode, provider, site, name, timezone, goals, settings, credentials,
	status, status_note, read_at, next_read_at, created_at`

func scanAnalyticsSource(r pgx.Row) (*model.AnalyticsSource, error) {
	var a model.AnalyticsSource
	err := r.Scan(&a.ID, &a.OrgID, &a.BrandID, &a.Livemode, &a.Provider, &a.Site, &a.Name, &a.Timezone, &a.Goals, &a.Settings,
		&a.Credentials, &a.Status, &a.StatusNote, &a.ReadAt, &a.NextReadAt, &a.CreatedAt)
	return &a, mapErr(err)
}

func collectAnalyticsSources(rows pgx.Rows, err error) ([]*model.AnalyticsSource, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.AnalyticsSource, error) { return scanAnalyticsSource(r) })
}

// CreateAnalyticsSource stores a new source, due to be read at once. The
// same site on the same brand and mode is ErrConflict.
func (s *Store) CreateAnalyticsSource(ctx context.Context, a *model.AnalyticsSource) error {
	if a.Settings == nil {
		a.Settings = map[string]string{}
	}
	if a.Goals == nil {
		a.Goals = []string{}
	}
	return mapErr(s.q.QueryRow(ctx, `INSERT INTO analytics_sources (id, org_id, brand_id, livemode, provider, site, name, timezone, goals,
		settings, credentials, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING next_read_at, created_at`,
		a.ID, a.OrgID, a.BrandID, a.Livemode, a.Provider, a.Site, a.Name, a.Timezone, a.Goals, a.Settings, a.Credentials, a.Status).
		Scan(&a.NextReadAt, &a.CreatedAt))
}

// AnalyticsSource returns one of an org's sources.
func (s *Store) AnalyticsSource(ctx context.Context, orgID, id uuid.UUID) (*model.AnalyticsSource, error) {
	return scanAnalyticsSource(s.q.QueryRow(ctx, `SELECT `+analyticsSourceCols+` FROM analytics_sources WHERE org_id = $1 AND id = $2`, orgID, id))
}

// AnalyticsSources lists an org's sources in a mode, optionally for one brand.
func (s *Store) AnalyticsSources(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID) ([]*model.AnalyticsSource, error) {
	return collectAnalyticsSources(s.q.Query(ctx, `SELECT `+analyticsSourceCols+` FROM analytics_sources WHERE org_id = $1 AND livemode = $2
		AND ($3::uuid IS NULL OR brand_id = $3) ORDER BY brand_id, provider, site, id`, orgID, livemode, brandID))
}

// DeleteAnalyticsSource forgets a source and its counts.
func (s *Store) DeleteAnalyticsSource(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM analytics_sources WHERE org_id = $1 AND id = $2`, orgID, id)
}

// ClaimDueAnalyticsSources takes active sources of providers the caller
// reads that are due, in one org or (orgID nil) every org, and pushes their
// next read back by lease.
func (s *Store) ClaimDueAnalyticsSources(ctx context.Context, orgID *uuid.UUID, providers []string, now time.Time, lease time.Duration, limit int) ([]*model.AnalyticsSource, error) {
	return collectAnalyticsSources(s.q.Query(ctx, `UPDATE analytics_sources SET next_read_at = $3 WHERE id IN (
			SELECT id FROM analytics_sources WHERE status = 'active' AND next_read_at <= $2 AND ($1::uuid IS NULL OR org_id = $1)
				AND provider = ANY($5)
			ORDER BY next_read_at LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING `+analyticsSourceCols, orgID, now, now.Add(lease), limit, providers))
}

// SaveAnalyticsResults replaces a source's counts for the days from from
// to to (a revised day can lose a tag combination, so the days are
// rewritten whole) and schedules its next read. Rows that clean to the same
// tags are added together.
func (s *Store) SaveAnalyticsResults(ctx context.Context, orgID, sourceID uuid.UUID, from, to time.Time, rows []analytics.Row, readAt, next time.Time) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.q.Exec(ctx, `DELETE FROM analytics_results WHERE source_id = $1 AND day BETWEEN $2 AND $3`, sourceID, from, to); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.q.Exec(ctx, `INSERT INTO analytics_results (org_id, source_id, day, utm_source, utm_medium, utm_campaign, utm_content,
					goal, visitors, visits, events, read_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (source_id, day, utm_source, utm_medium, utm_campaign, utm_content, goal) DO UPDATE SET
					visitors = analytics_results.visitors + EXCLUDED.visitors, visits = analytics_results.visits + EXCLUDED.visits,
					events = analytics_results.events + EXCLUDED.events`,
				orgID, sourceID, r.Day, r.UTM.Source, r.UTM.Medium, r.UTM.Campaign, r.UTM.Content, r.Goal, r.Visitors, r.Visits, r.Events, readAt); err != nil {
				return mapErr(err)
			}
		}
		return tx.execOne(ctx, `UPDATE analytics_sources SET read_at = $3, next_read_at = $4, status_note = '', updated_at = now()
			WHERE org_id = $1 AND id = $2`, orgID, sourceID, readAt, next)
	})
}

// SetAnalyticsSourceStatus records why a source could not be read, and
// when to try again.
func (s *Store) SetAnalyticsSourceStatus(ctx context.Context, orgID, id uuid.UUID, status model.AdAccountStatus, note string, next time.Time) error {
	return s.execOne(ctx, `UPDATE analytics_sources SET status = $3, status_note = $4, next_read_at = $5, updated_at = now() WHERE org_id = $1 AND id = $2`,
		orgID, id, status, note, next)
}

// AnalyticsGroup is how an analytics summary groups counts.
type AnalyticsGroup string

// Groupings.
const (
	AnalyticsByPost     AnalyticsGroup = "post"
	AnalyticsBySource   AnalyticsGroup = "source"
	AnalyticsByMedium   AnalyticsGroup = "medium"
	AnalyticsByCampaign AnalyticsGroup = "campaign"
	AnalyticsByContent  AnalyticsGroup = "content"
	AnalyticsByDay      AnalyticsGroup = "day"
)

// AnalyticsFilter narrows an analytics summary to days Since through Until.
type AnalyticsFilter struct {
	GroupBy      AnalyticsGroup
	BrandID      *uuid.UUID
	Since, Until time.Time
	Limit        int
}

// AnalyticsRow is one group of a summary: daily visitors and visits added
// up, and conversions by goal.
type AnalyticsRow struct {
	Key         string
	Visitors    int64
	Visits      int64
	Conversions int64
	Goals       map[string]int64
}

// AnalyticsTotals are all visitors in a window and those who came untagged.
type AnalyticsTotals struct {
	Visitors, Untagged int64
}

const untaggedSQL = `r.utm_source = '' AND r.utm_medium = '' AND r.utm_campaign = '' AND r.utm_content = ''`

// postContent matches the utm_content Araldo writes on a post's links: the
// post's ID.
const postContent = `r.utm_content LIKE 'post\_%'`

// AnalyticsSummary adds up counts by group. Groups by tag leave out
// untagged traffic; by post, only tags naming a post count.
func (s *Store) AnalyticsSummary(ctx context.Context, orgID uuid.UUID, livemode bool, f AnalyticsFilter) ([]AnalyticsRow, AnalyticsTotals, error) {
	key, where, order := "", "NOT ("+untaggedSQL+")", "conversions DESC, visitors DESC, key"
	switch f.GroupBy {
	case AnalyticsByPost:
		key, where = "r.utm_content", postContent
	case AnalyticsBySource:
		key = "r.utm_source"
	case AnalyticsByMedium:
		key = "r.utm_medium"
	case AnalyticsByCampaign:
		key = "r.utm_campaign"
	case AnalyticsByContent:
		key = "r.utm_content"
	case AnalyticsByDay:
		key, where, order = "to_char(r.day, 'YYYY-MM-DD')", "true", "key"
	default:
		return nil, AnalyticsTotals{}, fmt.Errorf("store: unknown analytics grouping %q", f.GroupBy)
	}
	scope := `FROM analytics_results r JOIN analytics_sources a ON a.id = r.source_id
		WHERE r.org_id = $1 AND a.livemode = $2 AND ($3::uuid IS NULL OR a.brand_id = $3) AND r.day BETWEEN $4 AND $5`
	args := []any{orgID, livemode, f.BrandID, f.Since, f.Until}
	var totals AnalyticsTotals
	if err := s.q.QueryRow(ctx, `SELECT COALESCE(sum(r.visitors) FILTER (WHERE r.goal = ''), 0),
		COALESCE(sum(r.visitors) FILTER (WHERE r.goal = '' AND `+untaggedSQL+`), 0) `+scope, args...).Scan(&totals.Visitors, &totals.Untagged); err != nil {
		return nil, totals, err
	}
	rows, err := s.q.Query(ctx, `SELECT `+key+` AS key, COALESCE(sum(r.visitors) FILTER (WHERE r.goal = ''), 0) AS visitors,
			COALESCE(sum(r.visits) FILTER (WHERE r.goal = ''), 0), COALESCE(sum(r.visitors) FILTER (WHERE r.goal <> ''), 0) AS conversions
		`+scope+` AND `+where+` GROUP BY 1 ORDER BY `+order+` LIMIT $6`, append(args, f.Limit)...)
	if err != nil {
		return nil, totals, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AnalyticsRow, error) {
		var row AnalyticsRow
		err := r.Scan(&row.Key, &row.Visitors, &row.Visits, &row.Conversions)
		row.Goals = map[string]int64{}
		return row, err
	})
	if err != nil || len(out) == 0 {
		return out, totals, err
	}
	keys := make([]string, len(out))
	index := map[string]int{}
	for i, r := range out {
		keys[i], index[r.Key] = r.Key, i
	}
	goals, err := s.q.Query(ctx, `SELECT `+key+` AS key, r.goal, sum(r.visitors) `+scope+` AND r.goal <> '' AND `+key+` = ANY($6)
		GROUP BY 1, 2`, append(args, keys)...)
	if err != nil {
		return nil, totals, err
	}
	defer goals.Close()
	for goals.Next() {
		var k, goal string
		var n int64
		if err := goals.Scan(&k, &goal, &n); err != nil {
			return nil, totals, err
		}
		if i, ok := index[k]; ok {
			out[i].Goals[goal] = n
		}
	}
	return out, totals, goals.Err()
}
