// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Developer apps and OAuth sign-ins (ADR 0021).

const appCols = `id, org_id, provider, name, client_id, client_secret, created_by, created_at`

func scanApp(r pgx.Row) (*model.ProviderApp, error) {
	var a model.ProviderApp
	var provider string
	err := r.Scan(&a.ID, &a.OrgID, &provider, &a.Name, &a.ClientID, &a.ClientSecret, &a.CreatedBy, &a.CreatedAt)
	a.Provider = platform.Provider(provider)
	return &a, mapErr(err)
}

// CreateProviderApp inserts an app.
func (s *Store) CreateProviderApp(ctx context.Context, a *model.ProviderApp) error {
	_, err := s.q.Exec(ctx, `INSERT INTO provider_apps (id, org_id, provider, name, client_id, client_secret, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, a.ID, a.OrgID, string(a.Provider), a.Name, a.ClientID, a.ClientSecret, a.CreatedBy)
	return mapErr(err)
}

// ProviderApp returns one of an org's apps.
func (s *Store) ProviderApp(ctx context.Context, orgID, id uuid.UUID) (*model.ProviderApp, error) {
	return scanApp(s.q.QueryRow(ctx, `SELECT `+appCols+` FROM provider_apps WHERE org_id = $1 AND id = $2`, orgID, id))
}

// ProviderApps lists an org's apps, by provider and name.
func (s *Store) ProviderApps(ctx context.Context, orgID uuid.UUID) ([]*model.ProviderApp, error) {
	rows, err := s.q.Query(ctx, `SELECT `+appCols+` FROM provider_apps WHERE org_id = $1 ORDER BY provider, name`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.ProviderApp, error) { return scanApp(r) })
}

// DeleteProviderApp deletes an app; channels connected through it keep
// working until their tokens need refreshing.
func (s *Store) DeleteProviderApp(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM provider_apps WHERE org_id = $1 AND id = $2`, orgID, id)
}

// CreateOAuthState records a sign-in in progress under the hash of its
// state parameter.
func (s *Store) CreateOAuthState(ctx context.Context, hash []byte, st *model.OAuthState) error {
	_, err := s.q.Exec(ctx, `INSERT INTO oauth_states (state_hash, org_id, user_id, brand_id, app_id, provider, verifier)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, hash, st.OrgID, st.UserID, st.BrandID, st.AppID, string(st.Provider), st.Verifier)
	return mapErr(err)
}

// OAuthState reads a sign-in created after since.
func (s *Store) OAuthState(ctx context.Context, hash []byte, since time.Time) (*model.OAuthState, error) {
	var st model.OAuthState
	var provider string
	err := s.q.QueryRow(ctx, `SELECT org_id, user_id, brand_id, app_id, provider, verifier, connections, created_at FROM oauth_states
		WHERE state_hash = $1 AND created_at > $2`, hash, since).
		Scan(&st.OrgID, &st.UserID, &st.BrandID, &st.AppID, &provider, &st.Verifier, &st.Connections, &st.CreatedAt)
	st.Provider = platform.Provider(provider)
	return &st, mapErr(err)
}

// SetOAuthConnections keeps a sign-in's encrypted connections for the
// member to choose among.
func (s *Store) SetOAuthConnections(ctx context.Context, hash, connections []byte) error {
	return s.execOne(ctx, `UPDATE oauth_states SET connections = $2 WHERE state_hash = $1`, hash, connections)
}

// DeleteOAuthState ends a sign-in; a state is used once.
func (s *Store) DeleteOAuthState(ctx context.Context, hash []byte) error {
	return s.execOne(ctx, `DELETE FROM oauth_states WHERE state_hash = $1`, hash)
}

// PruneOAuthStates deletes sign-ins started before cutoff.
func (s *Store) PruneOAuthStates(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM oauth_states WHERE created_at < $1`, cutoff)
	return int(tag.RowsAffected()), err
}
