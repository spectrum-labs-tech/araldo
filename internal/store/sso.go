// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Single sign-on (ADR 0033).

// SaveSSOConnection creates or replaces an org's identity provider. A nil
// secret keeps the one stored.
func (s *Store) SaveSSOConnection(ctx context.Context, c *model.SSOConnection, secret []byte) error {
	if secret == nil {
		return s.execOne(ctx, `UPDATE sso_connections SET issuer = $2, client_id = $3, default_role = $4, updated_at = now() WHERE org_id = $1`,
			c.OrgID, c.Issuer, c.ClientID, c.DefaultRole)
	}
	_, err := s.q.Exec(ctx, `INSERT INTO sso_connections (org_id, issuer, client_id, client_secret, default_role) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id) DO UPDATE SET issuer = $2, client_id = $3, client_secret = $4, default_role = $5, updated_at = now()`,
		c.OrgID, c.Issuer, c.ClientID, secret, c.DefaultRole)
	return mapErr(err)
}

// SSOConnection returns an org's identity provider and its sealed secret.
func (s *Store) SSOConnection(ctx context.Context, orgID uuid.UUID) (*model.SSOConnection, []byte, error) {
	var c model.SSOConnection
	var secret []byte
	err := s.q.QueryRow(ctx, `SELECT org_id, issuer, client_id, client_secret, default_role, created_at, updated_at
		FROM sso_connections WHERE org_id = $1`, orgID).Scan(&c.OrgID, &c.Issuer, &c.ClientID, &secret, &c.DefaultRole, &c.CreatedAt, &c.UpdatedAt)
	return &c, secret, mapErr(err)
}

// DeleteSSOConnection removes an org's identity provider.
func (s *Store) DeleteSSOConnection(ctx context.Context, orgID uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM sso_connections WHERE org_id = $1`, orgID)
}

// AddSSODomain adds a domain for an org to verify; ErrConflict if the org
// has it already.
func (s *Store) AddSSODomain(ctx context.Context, d *model.SSODomain) error {
	_, err := s.q.Exec(ctx, `INSERT INTO sso_domains (org_id, domain, token) VALUES ($1, $2, $3)`, d.OrgID, d.Domain, d.Token)
	return mapErr(err)
}

// SSODomains lists an org's domains, by name.
func (s *Store) SSODomains(ctx context.Context, orgID uuid.UUID) ([]*model.SSODomain, error) {
	rows, err := s.q.Query(ctx, `SELECT org_id, domain, token, verified_at, created_at FROM sso_domains WHERE org_id = $1 ORDER BY domain`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.SSODomain, error) {
		var d model.SSODomain
		return &d, r.Scan(&d.OrgID, &d.Domain, &d.Token, &d.VerifiedAt, &d.CreatedAt)
	})
}

// VerifySSODomain marks an org's domain verified; ErrConflict if another
// org verified it first.
func (s *Store) VerifySSODomain(ctx context.Context, orgID uuid.UUID, domain string, at time.Time) error {
	return s.execOne(ctx, `UPDATE sso_domains SET verified_at = $3 WHERE org_id = $1 AND domain = $2`, orgID, domain, at)
}

// DeleteSSODomain removes one of an org's domains.
func (s *Store) DeleteSSODomain(ctx context.Context, orgID uuid.UUID, domain string) error {
	return s.execOne(ctx, `DELETE FROM sso_domains WHERE org_id = $1 AND domain = $2`, orgID, domain)
}

// SSODomainOrg finds the org that verified a domain.
func (s *Store) SSODomainOrg(ctx context.Context, domain string) (uuid.UUID, error) {
	var orgID uuid.UUID
	err := s.q.QueryRow(ctx, `SELECT org_id FROM sso_domains WHERE domain = $1 AND verified_at IS NOT NULL`, domain).Scan(&orgID)
	return orgID, mapErr(err)
}

// SSOState is a sign-in waiting for the provider to send the person back.
type SSOState struct {
	OrgID     uuid.UUID
	Verifier  string
	Nonce     string
	Next      string
	ExpiresAt time.Time
}

// CreateSSOState stores a sign-in under its state's hash.
func (s *Store) CreateSSOState(ctx context.Context, hash []byte, st *SSOState) error {
	_, err := s.q.Exec(ctx, `INSERT INTO sso_states (state_hash, org_id, verifier, nonce, next, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		hash, st.OrgID, st.Verifier, st.Nonce, st.Next, st.ExpiresAt)
	return mapErr(err)
}

// TakeSSOState returns a sign-in and deletes it, so it is used once, if it
// has not expired at now.
func (s *Store) TakeSSOState(ctx context.Context, hash []byte, now time.Time) (*SSOState, error) {
	var st SSOState
	err := s.q.QueryRow(ctx, `DELETE FROM sso_states WHERE state_hash = $1 AND expires_at > $2 RETURNING org_id, verifier, nonce, next, expires_at`,
		hash, now).Scan(&st.OrgID, &st.Verifier, &st.Nonce, &st.Next, &st.ExpiresAt)
	return &st, mapErr(err)
}

// PruneSSOStates deletes sign-ins never finished.
func (s *Store) PruneSSOStates(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM sso_states WHERE expires_at <= $1`, now)
	return int(tag.RowsAffected()), err
}
