// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The dashboard's overview: what needs someone, what is coming up and what
// the last days brought, each bounded.

// UpcomingPosts lists posts scheduled or waiting for approval with a
// publish time in [from, to), soonest first, at most limit.
func (s *Store) UpcomingPosts(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID, from, to time.Time, limit int) ([]*model.Post, error) {
	return s.postsWhere(ctx, orgID, ` WHERE p.org_id = $1 AND p.livemode = $2 AND ($3::uuid IS NULL OR p.brand_id = $3)
		AND p.status IN ('scheduled', 'pending_approval') AND p.publish_at >= $4 AND p.publish_at < $5
		ORDER BY p.publish_at, p.id LIMIT $6`, orgID, livemode, brandID, from, to, limit)
}

// PostsNeedingAttention lists posts with a copy needing attention, or that
// failed or partly published since since, newest first, at most limit.
func (s *Store) PostsNeedingAttention(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID, since time.Time, limit int) ([]*model.Post, error) {
	return s.postsWhere(ctx, orgID, ` WHERE p.org_id = $1 AND p.livemode = $2 AND ($3::uuid IS NULL OR p.brand_id = $3)
		AND (EXISTS (SELECT 1 FROM post_targets t WHERE t.org_id = p.org_id AND t.post_id = p.id AND t.status = 'needs_attention')
			OR (p.status IN ('failed', 'partially_published') AND p.updated_at >= $4))
		ORDER BY p.id DESC LIMIT $5`, orgID, livemode, brandID, since, limit)
}

func (s *Store) postsWhere(ctx context.Context, orgID uuid.UUID, where string, args ...any) ([]*model.Post, error) {
	var out []*model.Post
	err := s.snapshot(ctx, func(tx *Store) error {
		rows, err := tx.q.Query(ctx, `SELECT `+prefixedPostCols+` FROM posts p`+where, args...)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Post, error) { return scanPost(r) }); err != nil {
			return err
		}
		return tx.attachTargets(ctx, orgID, out)
	})
	return out, err
}

// UpcomingIssues lists issues scheduled, sending or waiting for approval
// with a send time in [from, to), soonest first, at most limit.
func (s *Store) UpcomingIssues(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID, from, to time.Time, limit int) ([]*model.Issue, error) {
	return s.issuesWhere(ctx, orgID, ` WHERE org_id = $1 AND livemode = $2 AND ($3::uuid IS NULL OR brand_id = $3)
		AND status IN ('scheduled', 'sending', 'pending_approval') AND send_at >= $4 AND send_at < $5
		ORDER BY send_at, id LIMIT $6`, orgID, livemode, brandID, from, to, limit)
}

// IssuesNeedingAttention lists issues with a delivery needing attention,
// or that failed or partly sent since since, newest first, at most limit.
func (s *Store) IssuesNeedingAttention(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID, since time.Time, limit int) ([]*model.Issue, error) {
	return s.issuesWhere(ctx, orgID, ` WHERE org_id = $1 AND livemode = $2 AND ($3::uuid IS NULL OR brand_id = $3)
		AND (EXISTS (SELECT 1 FROM newsletter_deliveries d WHERE d.org_id = newsletter_issues.org_id AND d.issue_id = newsletter_issues.id
				AND d.status = 'needs_attention')
			OR (status IN ('failed', 'partially_sent') AND updated_at >= $4))
		ORDER BY id DESC LIMIT $5`, orgID, livemode, brandID, since, limit)
}

func (s *Store) issuesWhere(ctx context.Context, orgID uuid.UUID, where string, args ...any) ([]*model.Issue, error) {
	var out []*model.Issue
	err := s.snapshot(ctx, func(tx *Store) error {
		rows, err := tx.q.Query(ctx, `SELECT `+issueCols+` FROM newsletter_issues`+where, args...)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Issue, error) { return scanIssue(r) }); err != nil {
			return err
		}
		return tx.attachIssueParts(ctx, orgID, out)
	})
	return out, err
}

// Activity is what a window brought: posts with a copy published in it,
// newsletter issues with a copy sent in it, and those copies' delivered
// emails and unique clicks as last read.
type Activity struct {
	PostsPublished int
	IssuesSent     int
	Delivered      int64
	Clicks         int64
}

// Activity counts what was published and sent since since.
func (s *Store) Activity(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID, since time.Time) (Activity, error) {
	var a Activity
	err := s.q.QueryRow(ctx, `SELECT
		(SELECT count(DISTINCT t.post_id) FROM post_targets t JOIN posts p ON p.org_id = t.org_id AND p.id = t.post_id
			WHERE t.org_id = $1 AND t.livemode = $2 AND ($3::uuid IS NULL OR p.brand_id = $3)
			AND t.status = 'published' AND t.published_at >= $4),
		count(DISTINCT d.issue_id), COALESCE(sum(d.delivered), 0), COALESCE(sum(d.clicks), 0)
		FROM newsletter_deliveries d JOIN newsletter_issues i ON i.org_id = d.org_id AND i.id = d.issue_id
		WHERE d.org_id = $1 AND i.livemode = $2 AND ($3::uuid IS NULL OR i.brand_id = $3) AND d.status = 'sent' AND d.sent_at >= $4`,
		orgID, livemode, brandID, since).Scan(&a.PostsPublished, &a.IssuesSent, &a.Delivered, &a.Clicks)
	return a, err
}
