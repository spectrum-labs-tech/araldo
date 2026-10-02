// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// StartEngagement schedules a published target's first engagement reading
// (ADR 0018); a target that has one already keeps it.
func (s *Store) StartEngagement(ctx context.Context, orgID, targetID uuid.UUID, at time.Time) error {
	_, err := s.q.Exec(ctx, `INSERT INTO target_engagement (target_id, org_id, next_read_at) VALUES ($1, $2, $3)
		ON CONFLICT (target_id) DO NOTHING`, targetID, orgID, at)
	return mapErr(err)
}

// EngagementDue is a target whose engagement should be read now.
type EngagementDue struct {
	TargetID    uuid.UUID
	OrgID       uuid.UUID
	ChannelID   uuid.UUID
	Provider    platform.Provider
	Posted      []platform.RemoteRef
	PublishedAt time.Time
}

// DueEngagement lists targets due for a reading, oldest due first, in one
// org or (orgID nil) every org.
func (s *Store) DueEngagement(ctx context.Context, orgID *uuid.UUID, now time.Time, limit int) ([]EngagementDue, error) {
	rows, err := s.q.Query(ctx, `SELECT t.id, t.org_id, t.channel_id, t.provider, t.posted, COALESCE(t.published_at, t.updated_at)
		FROM target_engagement e JOIN post_targets t ON t.id = e.target_id
		WHERE e.next_read_at <= $1 AND ($3::uuid IS NULL OR e.org_id = $3)
		ORDER BY e.next_read_at LIMIT $2`, now, limit, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EngagementDue, error) {
		var d EngagementDue
		var provider string
		err := r.Scan(&d.TargetID, &d.OrgID, &d.ChannelID, &provider, &d.Posted, &d.PublishedAt)
		d.Provider = platform.Provider(provider)
		return d, err
	})
}

// RecordEngagement stores a reading as the latest and in the history, and
// when to read next (nil: never, the schedule is over).
func (s *Store) RecordEngagement(ctx context.Context, orgID, targetID uuid.UUID, at time.Time, c platform.Counts, next *time.Time) error {
	state := model.EngagementCollecting
	if next == nil {
		state = model.EngagementDone
	}
	if err := s.execOne(ctx, `UPDATE target_engagement SET state = $3, likes = $4, reposts = $5, replies = $6, quotes = $7, views = $8,
		read_at = $9, next_read_at = $10, error = '' WHERE org_id = $1 AND target_id = $2`,
		orgID, targetID, state, c.Likes, c.Reposts, c.Replies, c.Quotes, c.Views, at, next); err != nil {
		return err
	}
	_, err := s.q.Exec(ctx, `INSERT INTO engagement_readings (org_id, target_id, read_at, likes, reposts, replies, quotes, views)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (target_id, read_at) DO NOTHING`,
		orgID, targetID, at, c.Likes, c.Reposts, c.Replies, c.Quotes, c.Views)
	return mapErr(err)
}

// SetEngagementState records a reading that did not happen: a new state
// (deleted, unsupported) or a retry later, with why.
func (s *Store) SetEngagementState(ctx context.Context, orgID, targetID uuid.UUID, state string, next *time.Time, msg string) error {
	return s.execOne(ctx, `UPDATE target_engagement SET state = $3, next_read_at = $4, error = $5 WHERE org_id = $1 AND target_id = $2`,
		orgID, targetID, state, next, msg)
}

// EngagementReadings lists a target's readings, oldest first.
func (s *Store) EngagementReadings(ctx context.Context, orgID, targetID uuid.UUID) ([]model.EngagementReading, error) {
	rows, err := s.q.Query(ctx, `SELECT read_at, likes, reposts, replies, quotes, views FROM engagement_readings
		WHERE org_id = $1 AND target_id = $2 ORDER BY read_at`, orgID, targetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.EngagementReading, error) {
		var e model.EngagementReading
		err := r.Scan(&e.ReadAt, &e.Likes, &e.Reposts, &e.Replies, &e.Quotes, &e.Views)
		return e, err
	})
}

// attachEngagement loads each target's latest engagement.
func (s *Store) attachEngagement(ctx context.Context, orgID uuid.UUID, posts []*model.Post) error {
	byID := map[uuid.UUID]*model.Target{}
	var ids []uuid.UUID
	for _, p := range posts {
		for i := range p.Targets {
			byID[p.Targets[i].ID] = &p.Targets[i]
			ids = append(ids, p.Targets[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.q.Query(ctx, `SELECT target_id, state, likes, reposts, replies, quotes, views, read_at, next_read_at, error
		FROM target_engagement WHERE org_id = $1 AND target_id = ANY($2)`, orgID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tid uuid.UUID
		var e model.Engagement
		if err := rows.Scan(&tid, &e.State, &e.Likes, &e.Reposts, &e.Replies, &e.Quotes, &e.Views, &e.ReadAt, &e.NextReadAt, &e.Error); err != nil {
			return err
		}
		byID[tid].Engagement = &e
	}
	return rows.Err()
}

// EngagementGroup is how a summary groups targets.
type EngagementGroup string

// Groupings.
const (
	GroupByPost     EngagementGroup = "post"
	GroupByChannel  EngagementGroup = "channel"
	GroupByTemplate EngagementGroup = "template"
)

// EngagementFilter picks the targets a summary covers: published in
// [Since, Until), in a brand if set.
type EngagementFilter struct {
	BrandID      *uuid.UUID
	Since, Until time.Time
	GroupBy      EngagementGroup
	Limit        int
}

// EngagementRow is one group's latest engagement, added up.
type EngagementRow struct {
	// ID is the post, channel or template; nil for posts without a
	// template.
	ID       *uuid.UUID
	Label    string
	Provider string
	Posts    int
	Targets  int
	platform.Counts
}

// EngagementSummary adds up the latest readings of published targets that
// have been read, by group, most engagement first.
func (s *Store) EngagementSummary(ctx context.Context, orgID uuid.UUID, livemode bool, f EngagementFilter) ([]EngagementRow, error) {
	var key, label, provider string
	switch f.GroupBy {
	case GroupByChannel:
		key, label, provider = "t.channel_id", "min(c.display_name)", "min(c.provider)"
	case GroupByTemplate:
		key, label, provider = "p.template_id", "COALESCE(min(tm.key), '')", "''"
	default:
		key, label, provider = "p.id", "COALESCE((array_agg(t.parts->>0 ORDER BY t.id))[1], '')", "''"
	}
	q := fmt.Sprintf(`SELECT %[1]s, %[2]s, %[3]s, count(DISTINCT p.id), count(*),
		sum(e.likes), sum(e.reposts), sum(e.replies), sum(e.quotes), sum(e.views)
		FROM target_engagement e
		JOIN post_targets t ON t.id = e.target_id
		JOIN posts p ON p.id = t.post_id
		JOIN channels c ON c.id = t.channel_id
		LEFT JOIN templates tm ON tm.id = p.template_id
		WHERE e.org_id = $1 AND t.livemode = $2 AND e.read_at IS NOT NULL
		AND t.published_at >= $3 AND t.published_at < $4 AND ($5::uuid IS NULL OR p.brand_id = $5)
		GROUP BY %[1]s
		ORDER BY sum(e.likes + e.reposts + e.replies + e.quotes) DESC, count(*) DESC, %[1]s
		LIMIT $6`, key, label, provider)
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.q.Query(ctx, q, orgID, livemode, f.Since, f.Until, f.BrandID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EngagementRow, error) {
		var row EngagementRow
		err := r.Scan(&row.ID, &row.Label, &row.Provider, &row.Posts, &row.Targets,
			&row.Likes, &row.Reposts, &row.Replies, &row.Quotes, &row.Views)
		return row, err
	})
}
