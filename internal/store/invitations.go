// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Invitations (ADR 0004).

const invitationCols = `i.id, i.org_id, o.name, i.email, i.role, i.invited_by, i.expires_at, i.accepted_at, i.created_at`

func scanInvitation(r pgx.Row) (*model.Invitation, error) {
	var inv model.Invitation
	err := r.Scan(&inv.ID, &inv.OrgID, &inv.OrgName, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	return &inv, mapErr(err)
}

// CreateInvitation stores inv, replacing an open invitation of the same
// person to the same org.
func (s *Store) CreateInvitation(ctx context.Context, inv *model.Invitation, emailNormalized string, tokenHash []byte) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.q.Exec(ctx, `DELETE FROM invitations WHERE org_id = $1 AND email_normalized = $2 AND accepted_at IS NULL`,
			inv.OrgID, emailNormalized); err != nil {
			return err
		}
		_, err := tx.q.Exec(ctx, `INSERT INTO invitations (id, org_id, email, email_normalized, role, token_hash, invited_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			inv.ID, inv.OrgID, inv.Email, emailNormalized, inv.Role, tokenHash, inv.InvitedBy, inv.ExpiresAt)
		return mapErr(err)
	})
}

// OpenInvitations lists an org's invitations not yet accepted, newest first.
func (s *Store) OpenInvitations(ctx context.Context, orgID uuid.UUID) ([]*model.Invitation, error) {
	rows, err := s.q.Query(ctx, `SELECT `+invitationCols+` FROM invitations i JOIN orgs o ON o.id = i.org_id
		WHERE i.org_id = $1 AND i.accepted_at IS NULL ORDER BY i.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Invitation, error) { return scanInvitation(r) })
}

// InvitationByToken finds an invitation by its token's hash.
func (s *Store) InvitationByToken(ctx context.Context, tokenHash []byte) (*model.Invitation, error) {
	return scanInvitation(s.q.QueryRow(ctx, `SELECT `+invitationCols+` FROM invitations i JOIN orgs o ON o.id = i.org_id
		WHERE i.token_hash = $1`, tokenHash))
}

// AcceptInvitation marks an open invitation accepted; ErrNotFound if it
// was accepted already.
func (s *Store) AcceptInvitation(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.execOne(ctx, `UPDATE invitations SET accepted_at = $2 WHERE id = $1 AND accepted_at IS NULL`, id, at)
}

// DeleteInvitation withdraws an open invitation.
func (s *Store) DeleteInvitation(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM invitations WHERE org_id = $1 AND id = $2 AND accepted_at IS NULL`, orgID, id)
}
