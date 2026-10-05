// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The operator API (ADR 0031): orgs across the install, their limits,
// status and usage, and the keys it is called with.

// SetOrgOperated saves what the operator sets on an org: its name, status
// and note, limits and external reference.
func (s *Store) SetOrgOperated(ctx context.Context, o *model.Org) error {
	return s.execOne(ctx, `UPDATE orgs SET name = $2, status = $3, status_note = $4, external_ref = NULLIF($5, ''), limit_brands = $6,
		limit_channels = $7, limit_members = $8, limit_posts_month = $9, updated_at = now() WHERE id = $1`,
		o.ID, o.Name, o.Status, o.StatusNote, o.ExternalRef, o.Limits.Brands, o.Limits.Channels, o.Limits.Members, o.Limits.PostsMonth)
}

// OrgByExternalRef finds the org with an external reference.
func (s *Store) OrgByExternalRef(ctx context.Context, ref string) (*model.Org, error) {
	return scanOrg(s.q.QueryRow(ctx, `SELECT `+orgCols+` FROM orgs WHERE external_ref = $1`, ref))
}

// Orgs lists the install's orgs, newest first, a page at a time.
func (s *Store) Orgs(ctx context.Context, p Page) ([]*model.Org, bool, error) {
	where, order, args := pageClause(p, "id", 1)
	rows, err := s.q.Query(ctx, `SELECT `+orgCols+` FROM orgs WHERE true`+where+order, args...)
	if err != nil {
		return nil, false, err
	}
	orgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Org, error) { return scanOrg(r) })
	if err != nil {
		return nil, false, err
	}
	orgs, more := trimPage(p, orgs)
	return orgs, more, nil
}

// CountBrands counts an org's brands.
func (s *Store) CountBrands(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM brands WHERE org_id = $1`, orgID).Scan(&n)
	return n, err
}

// CountLiveChannels counts an org's live channels.
func (s *Store) CountLiveChannels(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM channels WHERE org_id = $1 AND livemode`, orgID).Scan(&n)
	return n, err
}

// CountSeats counts an org's members and its invitations still open at
// now, but one to exceptEmail (normalized): the people it has or has asked
// in.
func (s *Store) CountSeats(ctx context.Context, orgID uuid.UUID, now time.Time, exceptEmail string) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT (SELECT count(*) FROM memberships WHERE org_id = $1)
		+ (SELECT count(*) FROM invitations WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > $2
			AND email_normalized <> $3)`, orgID, now, exceptEmail).Scan(&n)
	return n, err
}

// CountLivePosts counts an org's live posts created since.
func (s *Store) CountLivePosts(ctx context.Context, orgID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM posts WHERE org_id = $1 AND livemode AND created_at >= $2`, orgID, since).Scan(&n)
	return n, err
}

// OrgUsage reads what an org has at now, and did between start and end.
func (s *Store) OrgUsage(ctx context.Context, orgID uuid.UUID, start, end, now time.Time) (*model.OrgUsage, error) {
	u := model.OrgUsage{PeriodStart: start, PeriodEnd: end}
	err := s.q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM brands WHERE org_id = $1),
		(SELECT count(*) FROM channels WHERE org_id = $1 AND livemode),
		(SELECT count(*) FROM memberships WHERE org_id = $1)
			+ (SELECT count(*) FROM invitations WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > $4),
		(SELECT count(*) FROM posts WHERE org_id = $1 AND livemode AND created_at >= $2 AND created_at < $3),
		(SELECT count(*) FROM post_targets WHERE org_id = $1 AND livemode AND status = 'published'
			AND published_at >= $2 AND published_at < $3),
		(SELECT COALESCE(sum(size_bytes), 0) FROM media WHERE org_id = $1)`, orgID, start, end, now).
		Scan(&u.Brands, &u.Channels, &u.Members, &u.PostsCreated, &u.TargetsSent, &u.MediaBytes)
	return &u, mapErr(err)
}

// Operator keys.

const opKeyCols = `id, name, hint, created_at, last_used_at, revoked_at`

func scanOperatorKey(r pgx.Row) (*model.OperatorKey, error) {
	var k model.OperatorKey
	err := r.Scan(&k.ID, &k.Name, &k.Hint, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
	return &k, mapErr(err)
}

// CreateOperatorKey stores a key by its hash.
func (s *Store) CreateOperatorKey(ctx context.Context, k *model.OperatorKey, hash []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO operator_keys (id, name, key_hash, hint) VALUES ($1, $2, $3, $4)`, k.ID, k.Name, hash, k.Hint)
	return mapErr(err)
}

// OperatorKeyByHash finds a key, revoked or not.
func (s *Store) OperatorKeyByHash(ctx context.Context, hash []byte) (*model.OperatorKey, error) {
	return scanOperatorKey(s.q.QueryRow(ctx, `SELECT `+opKeyCols+` FROM operator_keys WHERE key_hash = $1`, hash))
}

// OperatorKeys lists the keys not revoked, newest first.
func (s *Store) OperatorKeys(ctx context.Context) ([]*model.OperatorKey, error) {
	rows, err := s.q.Query(ctx, `SELECT `+opKeyCols+` FROM operator_keys WHERE revoked_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.OperatorKey, error) { return scanOperatorKey(r) })
}

// RevokeOperatorKey revokes a key not already revoked.
func (s *Store) RevokeOperatorKey(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.execOne(ctx, `UPDATE operator_keys SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at)
}

// TouchOperatorKey records a key's use, at most once a minute.
func (s *Store) TouchOperatorKey(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE operator_keys SET last_used_at = $2 WHERE id = $1
		AND (last_used_at IS NULL OR last_used_at < $2 - interval '1 minute')`, id, at)
	return err
}
