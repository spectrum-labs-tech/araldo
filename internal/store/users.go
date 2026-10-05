// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Data keys (keyring.Store).

func (s *Store) CurrentDataKey(ctx context.Context, scope uuid.UUID) (keyring.WrappedKey, error) {
	return s.dataKey(ctx, `SELECT scope, version, kek_id, wrapped FROM data_keys WHERE scope = $1 ORDER BY version DESC LIMIT 1`, scope)
}

func (s *Store) DataKey(ctx context.Context, scope uuid.UUID, version int) (keyring.WrappedKey, error) {
	return s.dataKey(ctx, `SELECT scope, version, kek_id, wrapped FROM data_keys WHERE scope = $1 AND version = $2`, scope, version)
}

func (s *Store) dataKey(ctx context.Context, sql string, args ...any) (keyring.WrappedKey, error) {
	var k keyring.WrappedKey
	err := s.q.QueryRow(ctx, sql, args...).Scan(&k.Scope, &k.Version, &k.KEKID, &k.Wrapped)
	if err == pgx.ErrNoRows { //nolint:errorlint // pgx returns it unwrapped
		return k, keyring.ErrNoKey
	}
	return k, err
}

func (s *Store) InsertDataKey(ctx context.Context, k keyring.WrappedKey) error {
	_, err := s.q.Exec(ctx, `INSERT INTO data_keys (scope, version, kek_id, wrapped) VALUES ($1, $2, $3, $4)`,
		k.Scope, k.Version, k.KEKID, k.Wrapped)
	if err = mapErr(err); err != nil && isConflict(err) {
		return keyring.ErrConflict
	}
	return err
}

func (s *Store) DataKeys(ctx context.Context) ([]keyring.WrappedKey, error) {
	rows, err := s.q.Query(ctx, `SELECT scope, version, kek_id, wrapped FROM data_keys ORDER BY scope, version`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (keyring.WrappedKey, error) {
		var k keyring.WrappedKey
		err := r.Scan(&k.Scope, &k.Version, &k.KEKID, &k.Wrapped)
		return k, err
	})
}

func (s *Store) RewrapDataKey(ctx context.Context, k keyring.WrappedKey) error {
	return s.execOne(ctx, `UPDATE data_keys SET kek_id = $3, wrapped = $4 WHERE scope = $1 AND version = $2`,
		k.Scope, k.Version, k.KEKID, k.Wrapped)
}

// DeleteDataKeys crypto-shreds a scope (ADR 0008).
func (s *Store) DeleteDataKeys(ctx context.Context, scope uuid.UUID) error {
	_, err := s.q.Exec(ctx, `DELETE FROM data_keys WHERE scope = $1`, scope)
	return err
}

func isConflict(err error) bool { return err != nil && errorsIs(err, ErrConflict) }

// Users.

const userCols = `id, email, name, password_hash, totp_secret, totp_last_step, totp_enabled_at, failed_logins, locked_until, created_at,
	EXISTS (SELECT 1 FROM passkeys WHERE passkeys.user_id = users.id)`

func scanUser(r pgx.Row) (*model.User, error) {
	var u model.User
	var pw *string
	err := r.Scan(&u.ID, &u.Email, &u.Name, &pw, &u.TOTPSecret, &u.TOTPLastStep, &u.TOTPEnabledAt, &u.FailedLogins, &u.LockedUntil, &u.CreatedAt,
		&u.HasPasskey)
	if pw != nil {
		u.PasswordHash = *pw
	}
	return &u, mapErr(err)
}

// CreateUser inserts a user; a taken email is ErrConflict.
func (s *Store) CreateUser(ctx context.Context, u *model.User, normalizedEmail string) error {
	_, err := s.q.Exec(ctx, `INSERT INTO users (id, email, email_normalized, name, password_hash) VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		u.ID, u.Email, normalizedEmail, u.Name, u.PasswordHash)
	return mapErr(err)
}

func (s *Store) UserByEmail(ctx context.Context, normalizedEmail string) (*model.User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email_normalized = $1`, normalizedEmail))
}

func (s *Store) User(ctx context.Context, id uuid.UUID) (*model.User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

// UserForUpdate locks the user's row for the rest of the transaction.
func (s *Store) UserForUpdate(ctx context.Context, id uuid.UUID) (*model.User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1 FOR UPDATE`, id))
}

func (s *Store) SetPassword(ctx context.Context, id uuid.UUID, hash string) error {
	return s.execOne(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
}

func (s *Store) SetUserName(ctx context.Context, id uuid.UUID, name string) error {
	return s.execOne(ctx, `UPDATE users SET name = $2, updated_at = now() WHERE id = $1`, id, name)
}

// RecordLoginFailure counts a failed sign-in and locks the account for
// lock (if non-zero).
func (s *Store) RecordLoginFailure(ctx context.Context, id uuid.UUID, lockUntil *time.Time) error {
	return s.execOne(ctx, `UPDATE users SET failed_logins = failed_logins + 1, locked_until = COALESCE($2, locked_until) WHERE id = $1`, id, lockUntil)
}

func (s *Store) ResetLoginFailures(ctx context.Context, id uuid.UUID) error {
	return s.execOne(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = $1`, id)
}

// SetTOTP stores (or with nil, clears) the encrypted TOTP secret.
func (s *Store) SetTOTP(ctx context.Context, id uuid.UUID, secret []byte, enabledAt *time.Time) error {
	return s.execOne(ctx, `UPDATE users SET totp_secret = $2, totp_enabled_at = $3, totp_last_step = 0, updated_at = now() WHERE id = $1`,
		id, secret, enabledAt)
}

// AdvanceTOTPStep records a used TOTP step; it fails (ErrNotFound) if the
// step is not newer than the last, so a code works once.
func (s *Store) AdvanceTOTPStep(ctx context.Context, id uuid.UUID, step int64) error {
	return s.execOne(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, id, step)
}

// ReplaceRecoveryCodes drops the user's codes and stores new hashes.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes [][]byte) error {
	if _, err := s.q.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := s.q.Exec(ctx, `INSERT INTO recovery_codes (id, user_id, code_hash) VALUES ($1, $2, $3)`, uuid.Must(uuid.NewV7()), userID, h); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode marks a matching unused code as used.
func (s *Store) UseRecoveryCode(ctx context.Context, userID uuid.UUID, hash []byte) error {
	return s.execOne(ctx, `UPDATE recovery_codes SET used_at = now() WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, userID, hash)
}

func (s *Store) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// Sessions.

const sessionCols = `id, user_id, csrf_token, state, current_org, livemode, sudo_until, user_agent, ip, created_at, last_seen_at, expires_at`

func scanSession(r pgx.Row) (*model.Session, error) {
	var ss model.Session
	err := r.Scan(&ss.ID, &ss.UserID, &ss.CSRFToken, &ss.State, &ss.CurrentOrg, &ss.Livemode, &ss.SudoUntil, &ss.UserAgent, &ss.IP,
		&ss.CreatedAt, &ss.LastSeenAt, &ss.ExpiresAt)
	return &ss, mapErr(err)
}

func (s *Store) CreateSession(ctx context.Context, ss *model.Session, tokenHash []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, csrf_token, state, current_org, livemode, sudo_until, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		ss.ID, ss.UserID, tokenHash, ss.CSRFToken, ss.State, ss.CurrentOrg, ss.Livemode, ss.SudoUntil, ss.UserAgent, ss.IP, ss.ExpiresAt)
	return mapErr(err)
}

// SessionByToken returns an unexpired session.
func (s *Store) SessionByToken(ctx context.Context, tokenHash []byte, now time.Time) (*model.Session, error) {
	return scanSession(s.q.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions WHERE token_hash = $1 AND expires_at > $2`, tokenHash, now))
}

// TouchSession slides the idle expiry, never past the absolute limit.
func (s *Store) TouchSession(ctx context.Context, id uuid.UUID, now, idleUntil, absoluteLimit time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE sessions SET last_seen_at = $2, expires_at = LEAST($3, $4) WHERE id = $1`, id, now, idleUntil, absoluteLimit)
	return err
}

func (s *Store) ActivateSession(ctx context.Context, id uuid.UUID, sudoUntil time.Time) error {
	return s.execOne(ctx, `UPDATE sessions SET state = 'active', sudo_until = $2 WHERE id = $1`, id, sudoUntil)
}

func (s *Store) SetSessionSudo(ctx context.Context, id uuid.UUID, until time.Time) error {
	return s.execOne(ctx, `UPDATE sessions SET sudo_until = $2 WHERE id = $1`, id, until)
}

func (s *Store) SetSessionContext(ctx context.Context, id uuid.UUID, org *uuid.UUID, livemode bool) error {
	return s.execOne(ctx, `UPDATE sessions SET current_org = $2, livemode = $3 WHERE id = $1`, id, org, livemode)
}

func (s *Store) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := s.q.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// DeleteOtherSessions signs a user out everywhere but keep.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID, keep uuid.UUID) error {
	_, err := s.q.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, keep)
	return err
}

func (s *Store) Sessions(ctx context.Context, userID uuid.UUID, now time.Time) ([]*model.Session, error) {
	rows, err := s.q.Query(ctx, `SELECT `+sessionCols+` FROM sessions WHERE user_id = $1 AND expires_at > $2 ORDER BY last_seen_at DESC`, userID, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Session, error) { return scanSession(r) })
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now)
	return int(tag.RowsAffected()), err
}
