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
// RenameProviderApp changes an app's name.
func (s *Store) RenameProviderApp(ctx context.Context, orgID, id uuid.UUID, name string) error {
	return s.execOne(ctx, `UPDATE provider_apps SET name = $3 WHERE org_id = $1 AND id = $2`, orgID, id, name)
}

func (s *Store) DeleteProviderApp(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM provider_apps WHERE org_id = $1 AND id = $2`, orgID, id)
}

// appColumns splits an app reference into the app_id and install_app_id
// columns: at most one is set (ADR 0030).
func appColumns(id *uuid.UUID, install bool) (orgApp, installApp *uuid.UUID) {
	if id == nil {
		return nil, nil
	}
	if install {
		return nil, id
	}
	return id, nil
}

// appRef reads the two columns back as one reference.
const appRef = `COALESCE(app_id, install_app_id), install_app_id IS NOT NULL`

// CreateOAuthState records a sign-in in progress under the hash of its
// state parameter.
func (s *Store) CreateOAuthState(ctx context.Context, hash []byte, st *model.OAuthState) error {
	orgApp, installApp := appColumns(&st.AppID, st.InstallApp)
	_, err := s.q.Exec(ctx, `INSERT INTO oauth_states (state_hash, org_id, user_id, brand_id, app_id, install_app_id, provider, verifier)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, hash, st.OrgID, st.UserID, st.BrandID, orgApp, installApp, string(st.Provider), st.Verifier)
	return mapErr(err)
}

// OAuthState reads a sign-in created after since.
func (s *Store) OAuthState(ctx context.Context, hash []byte, since time.Time) (*model.OAuthState, error) {
	var st model.OAuthState
	var provider string
	err := s.q.QueryRow(ctx, `SELECT org_id, user_id, brand_id, `+appRef+`, provider, verifier, connections, created_at FROM oauth_states
		WHERE state_hash = $1 AND created_at > $2`, hash, since).
		Scan(&st.OrgID, &st.UserID, &st.BrandID, &st.AppID, &st.InstallApp, &provider, &st.Verifier, &st.Connections, &st.CreatedAt)
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

// Install apps (ADR 0030): developer apps the install provides to every org.

const installAppCols = `id, provider, name, client_id, client_secret, created_at`

func scanInstallApp(r pgx.Row) (*model.ProviderApp, error) {
	a := model.ProviderApp{Install: true}
	var provider string
	err := r.Scan(&a.ID, &provider, &a.Name, &a.ClientID, &a.ClientSecret, &a.CreatedAt)
	a.Provider = platform.Provider(provider)
	return &a, mapErr(err)
}

// CreateInstallApp inserts an install app.
func (s *Store) CreateInstallApp(ctx context.Context, a *model.ProviderApp) error {
	_, err := s.q.Exec(ctx, `INSERT INTO install_apps (id, provider, name, client_id, client_secret) VALUES ($1, $2, $3, $4, $5)`,
		a.ID, string(a.Provider), a.Name, a.ClientID, a.ClientSecret)
	return mapErr(err)
}

// InstallApp returns one install app.
func (s *Store) InstallApp(ctx context.Context, id uuid.UUID) (*model.ProviderApp, error) {
	return scanInstallApp(s.q.QueryRow(ctx, `SELECT `+installAppCols+` FROM install_apps WHERE id = $1`, id))
}

// InstallApps lists the install's apps, by provider and name.
func (s *Store) InstallApps(ctx context.Context) ([]*model.ProviderApp, error) {
	rows, err := s.q.Query(ctx, `SELECT `+installAppCols+` FROM install_apps ORDER BY provider, name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.ProviderApp, error) { return scanInstallApp(r) })
}

// RenameInstallApp changes an install app's name.
func (s *Store) RenameInstallApp(ctx context.Context, id uuid.UUID, name string) error {
	return s.execOne(ctx, `UPDATE install_apps SET name = $2, updated_at = now() WHERE id = $1`, id, name)
}

// DeleteInstallApp deletes an install app; what was connected through it
// keeps working until its token needs renewing.
func (s *Store) DeleteInstallApp(ctx context.Context, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM install_apps WHERE id = $1`, id)
}
