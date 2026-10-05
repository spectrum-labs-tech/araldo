// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Report shares (ADR 0026).

const shareCols = `id, org_id, brand_id, livemode, month, created_by, expires_at, revoked_at, created_at`

func scanShare(r pgx.Row) (*model.ReportShare, error) {
	var sh model.ReportShare
	err := r.Scan(&sh.ID, &sh.OrgID, &sh.BrandID, &sh.Livemode, &sh.Month, &sh.CreatedBy, &sh.ExpiresAt, &sh.RevokedAt, &sh.CreatedAt)
	return &sh, mapErr(err)
}

// CreateReportShare stores a share by its token's hash.
func (s *Store) CreateReportShare(ctx context.Context, sh *model.ReportShare, hash []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO report_shares (id, org_id, brand_id, livemode, month, token_hash, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, sh.ID, sh.OrgID, sh.BrandID, sh.Livemode, sh.Month, hash, sh.CreatedBy, sh.ExpiresAt)
	return mapErr(err)
}

// ReportShareByHash finds a share by its token's hash, whatever its state.
func (s *Store) ReportShareByHash(ctx context.Context, hash []byte) (*model.ReportShare, error) {
	return scanShare(s.q.QueryRow(ctx, `SELECT `+shareCols+` FROM report_shares WHERE token_hash = $1`, hash))
}

// ReportShares lists a brand's shares in a mode still open at now, newest
// first.
func (s *Store) ReportShares(ctx context.Context, orgID, brandID uuid.UUID, livemode bool, now time.Time) ([]*model.ReportShare, error) {
	rows, err := s.q.Query(ctx, `SELECT `+shareCols+` FROM report_shares WHERE org_id = $1 AND brand_id = $2 AND livemode = $3
		AND revoked_at IS NULL AND expires_at > $4 ORDER BY id DESC`, orgID, brandID, livemode, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.ReportShare, error) { return scanShare(r) })
}

// ReportShare returns one of an org's shares.
func (s *Store) ReportShare(ctx context.Context, orgID, id uuid.UUID) (*model.ReportShare, error) {
	return scanShare(s.q.QueryRow(ctx, `SELECT `+shareCols+` FROM report_shares WHERE org_id = $1 AND id = $2`, orgID, id))
}

// RevokeReportShare stops a share working.
func (s *Store) RevokeReportShare(ctx context.Context, orgID, id uuid.UUID, at time.Time) error {
	return s.execOne(ctx, `UPDATE report_shares SET revoked_at = $3 WHERE org_id = $1 AND id = $2 AND revoked_at IS NULL`, orgID, id, at)
}
