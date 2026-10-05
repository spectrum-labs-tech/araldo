// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// User tokens and device sign-ins (ADR 0028).

const utokCols = `id, user_id, livemode, name, hint, created_ip, last_used_at, revoked_at, created_at`

func scanUserToken(r pgx.Row) (*model.UserToken, error) {
	var t model.UserToken
	err := r.Scan(&t.ID, &t.UserID, &t.Livemode, &t.Name, &t.Hint, &t.CreatedIP, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt)
	return &t, mapErr(err)
}

// CreateUserToken stores a token by its hash.
func (s *Store) CreateUserToken(ctx context.Context, t *model.UserToken, tokenHash []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO user_tokens (id, user_id, livemode, name, token_hash, hint, created_ip) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.UserID, t.Livemode, t.Name, tokenHash, t.Hint, t.CreatedIP)
	return mapErr(err)
}

// UserTokenByHash finds a token, revoked or not.
func (s *Store) UserTokenByHash(ctx context.Context, tokenHash []byte) (*model.UserToken, error) {
	return scanUserToken(s.q.QueryRow(ctx, `SELECT `+utokCols+` FROM user_tokens WHERE token_hash = $1`, tokenHash))
}

// UserTokens lists a user's tokens not revoked, newest first.
func (s *Store) UserTokens(ctx context.Context, userID uuid.UUID) ([]*model.UserToken, error) {
	rows, err := s.q.Query(ctx, `SELECT `+utokCols+` FROM user_tokens WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.UserToken, error) { return scanUserToken(r) })
}

// TouchUserToken records use, at most once a minute.
func (s *Store) TouchUserToken(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE user_tokens SET last_used_at = $2 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2 - interval '1 minute')`, id, now)
	return err
}

// RevokeUserToken stops one of a user's tokens working.
func (s *Store) RevokeUserToken(ctx context.Context, userID, id uuid.UUID, at time.Time) error {
	return s.execOne(ctx, `UPDATE user_tokens SET revoked_at = $3 WHERE user_id = $1 AND id = $2 AND revoked_at IS NULL`, userID, id, at)
}

// RevokeAllUserTokens stops all of a user's tokens working.
func (s *Store) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID, at time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE user_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, at)
	return err
}

// PruneUserTokens deletes tokens revoked, or unused since before cutoff.
func (s *Store) PruneUserTokens(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM user_tokens WHERE revoked_at IS NOT NULL OR COALESCE(last_used_at, created_at) < $1`, cutoff)
	return int(tag.RowsAffected()), err
}

const deviceCols = `id, user_code, device_name, livemode, client_ip, status, user_id, poll_interval, last_polled_at, expires_at, created_at`

func scanDevice(r pgx.Row) (*model.DeviceAuthorization, error) {
	var d model.DeviceAuthorization
	err := r.Scan(&d.ID, &d.UserCode, &d.DeviceName, &d.Livemode, &d.ClientIP, &d.Status, &d.UserID, &d.PollInterval, &d.LastPolledAt,
		&d.ExpiresAt, &d.CreatedAt)
	return &d, mapErr(err)
}

// CreateDeviceAuthorization stores a device sign-in; ErrConflict if its
// user code is already waiting.
func (s *Store) CreateDeviceAuthorization(ctx context.Context, d *model.DeviceAuthorization, deviceCodeHash []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO device_authorizations (id, device_code_hash, user_code, device_name, livemode, client_ip, poll_interval, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, d.ID, deviceCodeHash, d.UserCode, d.DeviceName, d.Livemode, d.ClientIP, d.PollInterval, d.ExpiresAt)
	return mapErr(err)
}

// PendingDeviceByCode finds the device sign-in waiting with user code.
func (s *Store) PendingDeviceByCode(ctx context.Context, userCode string) (*model.DeviceAuthorization, error) {
	return scanDevice(s.q.QueryRow(ctx, `SELECT `+deviceCols+` FROM device_authorizations WHERE user_code = $1 AND status = 'pending'`, userCode))
}

// DecideDevice approves (with the user) or denies a waiting sign-in;
// ErrNotFound if it is no longer waiting.
func (s *Store) DecideDevice(ctx context.Context, id uuid.UUID, status string, userID uuid.UUID) error {
	return s.execOne(ctx, `UPDATE device_authorizations SET status = $2, user_id = $3 WHERE id = $1 AND status = 'pending'`, id, status, userID)
}

// PollDevice finds a sign-in by its device code, locked for the caller's
// transaction, and records the poll; it returns when it was last polled
// before this.
func (s *Store) PollDevice(ctx context.Context, deviceCodeHash []byte, now time.Time) (*model.DeviceAuthorization, error) {
	d, err := scanDevice(s.q.QueryRow(ctx, `SELECT `+deviceCols+` FROM device_authorizations WHERE device_code_hash = $1 FOR UPDATE`, deviceCodeHash))
	if err != nil {
		return nil, err
	}
	_, err = s.q.Exec(ctx, `UPDATE device_authorizations SET last_polled_at = $2 WHERE id = $1`, d.ID, now)
	return d, err
}

// SlowDevice raises a sign-in's polling interval, for a CLI polling too fast.
func (s *Store) SlowDevice(ctx context.Context, id uuid.UUID, interval int) error {
	return s.execOne(ctx, `UPDATE device_authorizations SET poll_interval = $2 WHERE id = $1`, id, interval)
}

// IssueDevice marks an approved sign-in's token issued, once.
func (s *Store) IssueDevice(ctx context.Context, id uuid.UUID) error {
	return s.execOne(ctx, `UPDATE device_authorizations SET status = 'issued' WHERE id = $1 AND status = 'approved'`, id)
}

// PruneDevices deletes sign-ins that expired before cutoff.
func (s *Store) PruneDevices(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM device_authorizations WHERE expires_at < $1`, cutoff)
	return int(tag.RowsAffected()), err
}
