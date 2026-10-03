// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Reports (ADR 0026) are computed from what is already stored; these are
// the counts no other read gives.

// NetworkCount is how many of a brand's targets on one network published,
// and failed, in a window.
type NetworkCount struct {
	Provider          string
	Published, Failed int64
}

// PublishingCounts counts a brand's targets published in [from, to), and
// those that failed or need attention, by when they last changed, by
// network.
func (s *Store) PublishingCounts(ctx context.Context, orgID uuid.UUID, livemode bool, brandID uuid.UUID, from, to time.Time) ([]NetworkCount, error) {
	rows, err := s.q.Query(ctx, `SELECT t.provider,
			count(*) FILTER (WHERE t.status = 'published' AND t.published_at >= $4 AND t.published_at < $5),
			count(*) FILTER (WHERE t.status IN ('failed', 'needs_attention') AND t.updated_at >= $4 AND t.updated_at < $5)
		FROM post_targets t JOIN posts p ON p.id = t.post_id
		WHERE t.org_id = $1 AND t.livemode = $2 AND p.brand_id = $3
			AND (t.published_at >= $4 AND t.published_at < $5 OR t.updated_at >= $4 AND t.updated_at < $5)
		GROUP BY t.provider ORDER BY 2 DESC, t.provider`, orgID, livemode, brandID, from, to)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (NetworkCount, error) {
		var n NetworkCount
		err := r.Scan(&n.Provider, &n.Published, &n.Failed)
		return n, err
	})
	if err != nil {
		return nil, err
	}
	kept := out[:0]
	for _, n := range out {
		if n.Published+n.Failed > 0 {
			kept = append(kept, n)
		}
	}
	return kept, nil
}

// IssuesSent lists a brand's issues with a delivery sent in [from, to),
// with their deliveries, most recently sent first.
func (s *Store) IssuesSent(ctx context.Context, orgID uuid.UUID, livemode bool, brandID uuid.UUID, from, to time.Time) ([]*model.Issue, error) {
	var out []*model.Issue
	err := s.snapshot(ctx, func(tx *Store) error {
		rows, err := tx.q.Query(ctx, `SELECT `+issueCols+` FROM newsletter_issues i WHERE i.org_id = $1 AND i.livemode = $2 AND i.brand_id = $3
			AND EXISTS (SELECT 1 FROM newsletter_deliveries d WHERE d.issue_id = i.id AND d.sent_at >= $4 AND d.sent_at < $5)
			ORDER BY i.send_at DESC, i.id DESC LIMIT 100`, orgID, livemode, brandID, from, to)
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
