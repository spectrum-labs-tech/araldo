// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

const channelCols = `id, org_id, brand_id, livemode, provider, COALESCE(emulates, ''), display_name, handle, external_id, profile_url,
	settings, credentials, status, status_note, hold_until, created_at, app_id, token_expires_at, checked_at, check_error`

func scanChannel(r pgx.Row) (*model.Channel, error) {
	var c model.Channel
	var provider, emulates string
	err := r.Scan(&c.ID, &c.OrgID, &c.BrandID, &c.Livemode, &provider, &emulates, &c.DisplayName, &c.Handle, &c.ExternalID, &c.ProfileURL,
		&c.Settings, &c.Credentials, &c.Status, &c.StatusNote, &c.HoldUntil, &c.CreatedAt, &c.AppID, &c.TokenExpiresAt, &c.CheckedAt, &c.CheckError)
	c.Provider, c.Emulates = platform.Provider(provider), platform.Provider(emulates)
	return &c, mapErr(err)
}

func (s *Store) CreateChannel(ctx context.Context, c *model.Channel) error {
	var emulates *string
	if c.Emulates != "" {
		e := string(c.Emulates)
		emulates = &e
	}
	if c.Settings == nil {
		c.Settings = map[string]string{}
	}
	_, err := s.q.Exec(ctx, `INSERT INTO channels (id, org_id, brand_id, livemode, provider, emulates, display_name, handle, external_id,
		profile_url, settings, credentials, status, app_id, token_expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		c.ID, c.OrgID, c.BrandID, c.Livemode, string(c.Provider), emulates, c.DisplayName, c.Handle, c.ExternalID, c.ProfileURL,
		c.Settings, c.Credentials, c.Status, c.AppID, c.TokenExpiresAt)
	return mapErr(err)
}

func (s *Store) Channel(ctx context.Context, orgID, id uuid.UUID) (*model.Channel, error) {
	return scanChannel(s.q.QueryRow(ctx, `SELECT `+channelCols+` FROM channels WHERE org_id = $1 AND id = $2`, orgID, id))
}

// Channels lists channels in an org and mode, optionally for one brand.
func (s *Store) Channels(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID) ([]*model.Channel, error) {
	rows, err := s.q.Query(ctx, `SELECT `+channelCols+` FROM channels WHERE org_id = $1 AND livemode = $2 AND ($3::uuid IS NULL OR brand_id = $3)
		ORDER BY brand_id, provider, display_name, id`, orgID, livemode, brandID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Channel, error) { return scanChannel(r) })
}

// UpdateChannelConnection stores new account details and credentials.
func (s *Store) UpdateChannelConnection(ctx context.Context, c *model.Channel) error {
	return s.execOne(ctx, `UPDATE channels SET display_name = $3, handle = $4, external_id = $5, profile_url = $6, settings = $7,
		credentials = $8, status = $9, status_note = $10, app_id = $11, token_expires_at = $12, updated_at = now() WHERE org_id = $1 AND id = $2`,
		c.OrgID, c.ID, c.DisplayName, c.Handle, c.ExternalID, c.ProfileURL, c.Settings, c.Credentials, c.Status, c.StatusNote,
		c.AppID, c.TokenExpiresAt)
}

// ChannelForUpdate reads a channel and locks it until the transaction ends.
func (s *Store) ChannelForUpdate(ctx context.Context, orgID, id uuid.UUID) (*model.Channel, error) {
	return scanChannel(s.q.QueryRow(ctx, `SELECT `+channelCols+` FROM channels WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
}

// SetChannelToken stores refreshed credentials and their expiry.
func (s *Store) SetChannelToken(ctx context.Context, orgID, id uuid.UUID, credentials []byte, expires *time.Time) error {
	return s.execOne(ctx, `UPDATE channels SET credentials = $3, token_expires_at = $4, updated_at = now() WHERE org_id = $1 AND id = $2`,
		orgID, id, credentials, expires)
}

// ChannelsExpiring lists active channels connected through an app whose
// token expires before cutoff, in one org or (orgID nil) every org.
func (s *Store) ChannelsExpiring(ctx context.Context, orgID *uuid.UUID, cutoff time.Time, limit int) ([]*model.Channel, error) {
	rows, err := s.q.Query(ctx, `SELECT `+channelCols+` FROM channels WHERE app_id IS NOT NULL AND status = 'active'
		AND token_expires_at < $1 AND ($3::uuid IS NULL OR org_id = $3) ORDER BY token_expires_at LIMIT $2`, cutoff, limit, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Channel, error) { return scanChannel(r) })
}

func (s *Store) SetChannelStatus(ctx context.Context, orgID, id uuid.UUID, status model.ChannelStatus, note string) error {
	return s.execOne(ctx, `UPDATE channels SET status = $3, status_note = $4, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, status, note)
}

// HoldChannel pauses publishing to a channel until t (after a rate limit).
func (s *Store) HoldChannel(ctx context.Context, orgID, id uuid.UUID, until time.Time) error {
	return s.execOne(ctx, `UPDATE channels SET hold_until = GREATEST(COALESCE(hold_until, $3), $3) WHERE org_id = $1 AND id = $2`, orgID, id, until)
}

func (s *Store) DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM channels WHERE org_id = $1 AND id = $2`, orgID, id)
}

// Templates.

const templateCols = `id, org_id, brand_id, key, name, approval, latest_version, created_at, updated_at`

func scanTemplate(r pgx.Row) (*model.Template, error) {
	var t model.Template
	err := r.Scan(&t.ID, &t.OrgID, &t.BrandID, &t.Key, &t.Name, &t.Approval, &t.LatestVersion, &t.CreatedAt, &t.UpdatedAt)
	return &t, mapErr(err)
}

func (s *Store) CreateTemplate(ctx context.Context, t *model.Template) error {
	if t.Approval == "" {
		t.Approval = model.TemplateApprovalInherit
	}
	_, err := s.q.Exec(ctx, `INSERT INTO templates (id, org_id, brand_id, key, name, approval) VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.OrgID, t.BrandID, t.Key, t.Name, t.Approval)
	return mapErr(err)
}

func (s *Store) Template(ctx context.Context, orgID, id uuid.UUID) (*model.Template, error) {
	return scanTemplate(s.q.QueryRow(ctx, `SELECT `+templateCols+` FROM templates WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) TemplateByKey(ctx context.Context, orgID, brandID uuid.UUID, key string) (*model.Template, error) {
	return scanTemplate(s.q.QueryRow(ctx, `SELECT `+templateCols+` FROM templates WHERE org_id = $1 AND brand_id = $2 AND key = $3`, orgID, brandID, key))
}

// TemplateForUpdate locks a template row (to add a version).
func (s *Store) TemplateForUpdate(ctx context.Context, orgID, id uuid.UUID) (*model.Template, error) {
	return scanTemplate(s.q.QueryRow(ctx, `SELECT `+templateCols+` FROM templates WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
}

func (s *Store) Templates(ctx context.Context, orgID uuid.UUID, brandID *uuid.UUID) ([]*model.Template, error) {
	rows, err := s.q.Query(ctx, `SELECT `+templateCols+` FROM templates WHERE org_id = $1 AND ($2::uuid IS NULL OR brand_id = $2)
		ORDER BY key, id`, orgID, brandID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Template, error) { return scanTemplate(r) })
}

func (s *Store) SetTemplateName(ctx context.Context, orgID, id uuid.UUID, name string) error {
	return s.execOne(ctx, `UPDATE templates SET name = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, name)
}

func (s *Store) SetTemplateApproval(ctx context.Context, orgID, id uuid.UUID, a model.TemplateApproval) error {
	return s.execOne(ctx, `UPDATE templates SET approval = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, a)
}

func (s *Store) DeleteTemplate(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM templates WHERE org_id = $1 AND id = $2`, orgID, id)
}

// AddTemplateVersion stores v as the template's next version.
func (s *Store) AddTemplateVersion(ctx context.Context, v *model.TemplateVersion) error {
	examples, err := json.Marshal(v.Examples)
	if err != nil {
		return err
	}
	if v.Overrides == nil {
		v.Overrides = map[platform.Provider]string{}
	}
	if v.Fit == nil {
		v.Fit = map[platform.Provider]platform.Fit{}
	}
	var variables any
	if len(v.Variables) > 0 {
		variables = v.Variables
	}
	if _, err := s.q.Exec(ctx, `INSERT INTO template_versions (org_id, template_id, version, variables, examples, body, overrides, fit, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		v.OrgID, v.TemplateID, v.Version, variables, examples, v.Body, v.Overrides, v.Fit, v.CreatedBy); err != nil {
		return mapErr(err)
	}
	return s.execOne(ctx, `UPDATE templates SET latest_version = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, v.OrgID, v.TemplateID, v.Version)
}

func (s *Store) TemplateVersion(ctx context.Context, orgID, templateID uuid.UUID, version int) (*model.TemplateVersion, error) {
	var v model.TemplateVersion
	var variables, examples []byte
	err := s.q.QueryRow(ctx, `SELECT org_id, template_id, version, variables, examples, body, overrides, fit, created_by, created_at
		FROM template_versions WHERE org_id = $1 AND template_id = $2 AND version = $3`, orgID, templateID, version).
		Scan(&v.OrgID, &v.TemplateID, &v.Version, &variables, &examples, &v.Body, &v.Overrides, &v.Fit, &v.CreatedBy, &v.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	v.Variables = variables
	if len(examples) > 0 {
		if err := json.Unmarshal(examples, &v.Examples); err != nil {
			return nil, err
		}
	}
	return &v, nil
}

// ClaimChannelsToCheck takes active live channels on providers the caller
// has, last checked before cutoff (or never), in one org or (orgID nil)
// every org, and marks them
// checked now so no other worker takes them; SetChannelCheck records the
// outcome.
func (s *Store) ClaimChannelsToCheck(ctx context.Context, orgID *uuid.UUID, providers []string, now, cutoff time.Time, limit int) ([]*model.Channel, error) {
	rows, err := s.q.Query(ctx, `UPDATE channels SET checked_at = $2 WHERE id IN (
			SELECT id FROM channels WHERE status = 'active' AND livemode AND (checked_at IS NULL OR checked_at < $3)
				AND ($1::uuid IS NULL OR org_id = $1) AND provider = ANY($5)
			ORDER BY checked_at NULLS FIRST LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING `+channelCols, orgID, now, cutoff, limit, providers)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Channel, error) { return scanChannel(r) })
}

// SetChannelCheck records a health check's outcome ("" when it passed).
func (s *Store) SetChannelCheck(ctx context.Context, orgID, id uuid.UUID, at time.Time, problem string) error {
	return s.execOne(ctx, `UPDATE channels SET checked_at = $3, check_error = $4 WHERE org_id = $1 AND id = $2`, orgID, id, at, problem)
}
