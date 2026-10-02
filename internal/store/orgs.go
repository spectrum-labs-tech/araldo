// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func errorsIs(err, target error) bool { return errors.Is(err, target) }

// Orgs and memberships.

func (s *Store) CreateOrg(ctx context.Context, o *model.Org) error {
	_, err := s.q.Exec(ctx, `INSERT INTO orgs (id, name, require_mfa) VALUES ($1, $2, $3)`, o.ID, o.Name, o.RequireMFA)
	return mapErr(err)
}

func (s *Store) Org(ctx context.Context, id uuid.UUID) (*model.Org, error) {
	var o model.Org
	err := s.q.QueryRow(ctx, `SELECT id, name, require_mfa, created_at FROM orgs WHERE id = $1`, id).Scan(&o.ID, &o.Name, &o.RequireMFA, &o.CreatedAt)
	return &o, mapErr(err)
}

func (s *Store) UpdateOrg(ctx context.Context, o *model.Org) error {
	return s.execOne(ctx, `UPDATE orgs SET name = $2, require_mfa = $3, updated_at = now() WHERE id = $1`, o.ID, o.Name, o.RequireMFA)
}

func (s *Store) DeleteOrg(ctx context.Context, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM orgs WHERE id = $1`, id)
}

func (s *Store) AddMember(ctx context.Context, m *model.Membership) error {
	_, err := s.q.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, $3)`, m.OrgID, m.UserID, m.Role)
	return mapErr(err)
}

func (s *Store) SetMemberRole(ctx context.Context, orgID, userID uuid.UUID, role model.Role) error {
	return s.execOne(ctx, `UPDATE memberships SET role = $3 WHERE org_id = $1 AND user_id = $2`, orgID, userID, role)
}

func (s *Store) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID)
}

func (s *Store) Membership(ctx context.Context, orgID, userID uuid.UUID) (*model.Membership, error) {
	var m model.Membership
	err := s.q.QueryRow(ctx, `SELECT m.org_id, m.user_id, m.role, m.created_at, o.name
		FROM memberships m JOIN orgs o ON o.id = m.org_id WHERE m.org_id = $1 AND m.user_id = $2`, orgID, userID).
		Scan(&m.OrgID, &m.UserID, &m.Role, &m.CreatedAt, &m.OrgName)
	return &m, mapErr(err)
}

// UserMemberships lists the orgs a user belongs to, by org name.
func (s *Store) UserMemberships(ctx context.Context, userID uuid.UUID) ([]model.Membership, error) {
	rows, err := s.q.Query(ctx, `SELECT m.org_id, m.user_id, m.role, m.created_at, o.name
		FROM memberships m JOIN orgs o ON o.id = m.org_id WHERE m.user_id = $1 ORDER BY o.name, o.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Membership, error) {
		var m model.Membership
		err := r.Scan(&m.OrgID, &m.UserID, &m.Role, &m.CreatedAt, &m.OrgName)
		return m, err
	})
}

// Members lists an org's members, by email.
func (s *Store) Members(ctx context.Context, orgID uuid.UUID) ([]model.Membership, error) {
	rows, err := s.q.Query(ctx, `SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.name, u.totp_enabled_at IS NOT NULL
		FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1 ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Membership, error) {
		var m model.Membership
		err := r.Scan(&m.OrgID, &m.UserID, &m.Role, &m.CreatedAt, &m.UserEmail, &m.UserName, &m.UserMFA)
		return m, err
	})
}

func (s *Store) CountOwners(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'owner'`, orgID).Scan(&n)
	return n, err
}

// Brands.

const brandCols = `id, org_id, name, slug, timezone, approval_policy, utm_domains, created_at`

func scanBrand(r pgx.Row) (*model.Brand, error) {
	var b model.Brand
	err := r.Scan(&b.ID, &b.OrgID, &b.Name, &b.Slug, &b.Timezone, &b.ApprovalPolicy, &b.UTMDomains, &b.CreatedAt)
	return &b, mapErr(err)
}

func (s *Store) CreateBrand(ctx context.Context, b *model.Brand) error {
	_, err := s.q.Exec(ctx, `INSERT INTO brands (id, org_id, name, slug, timezone, approval_policy, utm_domains) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		b.ID, b.OrgID, b.Name, b.Slug, b.Timezone, b.ApprovalPolicy, nonNilStrings(b.UTMDomains))
	return mapErr(err)
}

func (s *Store) UpdateBrand(ctx context.Context, b *model.Brand) error {
	return mapErr(s.execOne(ctx, `UPDATE brands SET name = $3, slug = $4, timezone = $5, approval_policy = $6, utm_domains = $7, updated_at = now()
		WHERE org_id = $1 AND id = $2`, b.OrgID, b.ID, b.Name, b.Slug, b.Timezone, b.ApprovalPolicy, nonNilStrings(b.UTMDomains)))
}

func (s *Store) Brand(ctx context.Context, orgID, id uuid.UUID) (*model.Brand, error) {
	return s.brandWithSlots(ctx, s.q.QueryRow(ctx, `SELECT `+brandCols+` FROM brands WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) BrandBySlug(ctx context.Context, orgID uuid.UUID, slug string) (*model.Brand, error) {
	return s.brandWithSlots(ctx, s.q.QueryRow(ctx, `SELECT `+brandCols+` FROM brands WHERE org_id = $1 AND slug = $2`, orgID, slug))
}

// brandWithSlots reads a brand row and its slots.
func (s *Store) brandWithSlots(ctx context.Context, row pgx.Row) (*model.Brand, error) {
	b, err := scanBrand(row)
	if err != nil {
		return nil, err
	}
	b.Slots, err = s.Slots(ctx, b.OrgID, b.ID)
	return b, err
}

func (s *Store) Brands(ctx context.Context, orgID uuid.UUID) ([]*model.Brand, error) {
	rows, err := s.q.Query(ctx, `SELECT `+brandCols+` FROM brands WHERE org_id = $1 ORDER BY name, id`, orgID)
	if err != nil {
		return nil, err
	}
	brands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Brand, error) { return scanBrand(r) })
	if err != nil || len(brands) == 0 {
		return brands, err
	}
	byID := map[uuid.UUID]*model.Brand{}
	for _, b := range brands {
		byID[b.ID] = b
	}
	srows, err := s.q.Query(ctx, `SELECT id, org_id, brand_id, weekday, minute_of_day FROM schedule_slots
		WHERE org_id = $1 ORDER BY weekday, minute_of_day`, orgID)
	if err != nil {
		return nil, err
	}
	slots, err := pgx.CollectRows(srows, scanSlot)
	if err != nil {
		return nil, err
	}
	for _, sl := range slots {
		if b := byID[sl.BrandID]; b != nil {
			b.Slots = append(b.Slots, sl)
		}
	}
	return brands, nil
}

func (s *Store) DeleteBrand(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM brands WHERE org_id = $1 AND id = $2`, orgID, id)
}

// Slots.

func (s *Store) Slots(ctx context.Context, orgID, brandID uuid.UUID) ([]model.Slot, error) {
	rows, err := s.q.Query(ctx, `SELECT id, org_id, brand_id, weekday, minute_of_day FROM schedule_slots
		WHERE org_id = $1 AND brand_id = $2 ORDER BY weekday, minute_of_day`, orgID, brandID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSlot)
}

func scanSlot(r pgx.CollectableRow) (model.Slot, error) {
	var sl model.Slot
	var wd int16
	err := r.Scan(&sl.ID, &sl.OrgID, &sl.BrandID, &wd, &sl.MinuteOfDay)
	sl.Weekday = time.Weekday(wd)
	return sl, err
}

// ReplaceSlots sets a brand's weekly slots.
func (s *Store) ReplaceSlots(ctx context.Context, orgID, brandID uuid.UUID, slots []model.Slot) error {
	if _, err := s.q.Exec(ctx, `DELETE FROM schedule_slots WHERE org_id = $1 AND brand_id = $2`, orgID, brandID); err != nil {
		return err
	}
	for _, sl := range slots {
		if _, err := s.q.Exec(ctx, `INSERT INTO schedule_slots (id, org_id, brand_id, weekday, minute_of_day) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`, uuid.Must(uuid.NewV7()), orgID, brandID, int16(sl.Weekday), sl.MinuteOfDay); err != nil { //nolint:gosec // 0..6
			return mapErr(err)
		}
	}
	return nil
}

// SlotTaken reports whether a live post already holds the brand's slot at t.
func (s *Store) SlotTaken(ctx context.Context, brandID uuid.UUID, livemode bool, at time.Time) (bool, error) {
	var taken bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE brand_id = $1 AND livemode = $2 AND slot_at = $3
		AND status NOT IN ('canceled', 'rejected'))`, brandID, livemode, at).Scan(&taken)
	return taken, err
}

// API keys.

// apiKeyCols are column names, not a credential.
const apiKeyCols = `id, org_id, livemode, name, hint, scopes, brand_id, created_by, created_at, last_used_at, expires_at, revoked_at` //nolint:gosec // G101 false positive

func scanAPIKey(r pgx.Row) (*model.APIKey, error) {
	var k model.APIKey
	err := r.Scan(&k.ID, &k.OrgID, &k.Livemode, &k.Name, &k.Hint, &k.Scopes, &k.BrandID, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt)
	return &k, mapErr(err)
}

func (s *Store) CreateAPIKey(ctx context.Context, k *model.APIKey, hash []byte) error {
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	_, err := s.q.Exec(ctx, `INSERT INTO api_keys (id, org_id, livemode, name, hint, key_hash, scopes, brand_id, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		k.ID, k.OrgID, k.Livemode, k.Name, k.Hint, hash, k.Scopes, k.BrandID, k.CreatedBy, k.ExpiresAt)
	return mapErr(err)
}

// APIKeyByHash finds a key by its hash (any org: this is how a request
// learns its org).
func (s *Store) APIKeyByHash(ctx context.Context, hash []byte) (*model.APIKey, error) {
	return scanAPIKey(s.q.QueryRow(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = $1`, hash))
}

func (s *Store) APIKeys(ctx context.Context, orgID uuid.UUID) ([]*model.APIKey, error) {
	rows, err := s.q.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE org_id = $1 ORDER BY revoked_at IS NOT NULL, created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.APIKey, error) { return scanAPIKey(r) })
}

func (s *Store) APIKey(ctx context.Context, orgID, id uuid.UUID) (*model.APIKey, error) {
	return scanAPIKey(s.q.QueryRow(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE org_id = $1 AND id = $2`, orgID, id))
}

// ExpireAPIKey sets (or shortens) a key's expiry, for revoking and rolling.
func (s *Store) ExpireAPIKey(ctx context.Context, orgID, id uuid.UUID, at time.Time, revoke bool) error {
	if revoke {
		return s.execOne(ctx, `UPDATE api_keys SET revoked_at = $3 WHERE org_id = $1 AND id = $2 AND revoked_at IS NULL`, orgID, id, at)
	}
	return s.execOne(ctx, `UPDATE api_keys SET expires_at = LEAST(COALESCE(expires_at, $3), $3) WHERE org_id = $1 AND id = $2`, orgID, id, at)
}

// TouchAPIKey records use, at most once a minute per key.
func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2 - interval '1 minute')`, id, now)
	return err
}

// nonNilStrings stores an empty list as '{}', never NULL.
func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
