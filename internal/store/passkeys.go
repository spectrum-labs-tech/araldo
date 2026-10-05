// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Passkeys (ADR 0007). The credential is the WebAuthn library's own record,
// kept whole as JSON: its sign count and flags change as it is used.

// CreatePasskey stores a new passkey.
func (s *Store) CreatePasskey(ctx context.Context, p *model.Passkey, credentialID []byte, credential json.RawMessage) error {
	_, err := s.q.Exec(ctx, `INSERT INTO passkeys (id, user_id, credential_id, credential, name) VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.UserID, credentialID, credential, p.Name)
	return mapErr(err)
}

// Passkeys lists a user's passkeys, oldest first.
func (s *Store) Passkeys(ctx context.Context, userID uuid.UUID) ([]*model.Passkey, error) {
	rows, err := s.q.Query(ctx, `SELECT id, user_id, name, created_at, last_used_at FROM passkeys WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Passkey, error) {
		var p model.Passkey
		return &p, r.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	})
}

// PasskeyCredentials are a user's passkeys' credential records.
func (s *Store) PasskeyCredentials(ctx context.Context, userID uuid.UUID) ([]json.RawMessage, error) {
	rows, err := s.q.Query(ctx, `SELECT credential FROM passkeys WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
}

// PasskeyOwner finds whose a credential ID is.
func (s *Store) PasskeyOwner(ctx context.Context, credentialID []byte) (uuid.UUID, error) {
	var u uuid.UUID
	err := s.q.QueryRow(ctx, `SELECT user_id FROM passkeys WHERE credential_id = $1`, credentialID).Scan(&u)
	return u, mapErr(err)
}

// UsePasskey records a sign-in with a passkey: its updated record (sign
// count, flags) and when.
func (s *Store) UsePasskey(ctx context.Context, userID uuid.UUID, credentialID []byte, credential json.RawMessage, at time.Time) error {
	return s.execOne(ctx, `UPDATE passkeys SET credential = $3, last_used_at = $4 WHERE user_id = $1 AND credential_id = $2`,
		userID, credentialID, credential, at)
}

// DeletePasskey removes one of a user's passkeys.
func (s *Store) DeletePasskey(ctx context.Context, userID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM passkeys WHERE user_id = $1 AND id = $2`, userID, id)
}

// CreatePasskeyChallenge stores a ceremony's state under its token's hash.
func (s *Store) CreatePasskeyChallenge(ctx context.Context, hash []byte, userID *uuid.UUID, purpose string, session json.RawMessage,
	expires time.Time) error {
	_, err := s.q.Exec(ctx, `INSERT INTO passkey_challenges (token_hash, user_id, purpose, session, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		hash, userID, purpose, session, expires)
	return mapErr(err)
}

// TakePasskeyChallenge returns a ceremony's state and deletes it, so it is
// used once, if it is for purpose and has not expired at now.
func (s *Store) TakePasskeyChallenge(ctx context.Context, hash []byte, purpose string, now time.Time) (*uuid.UUID, json.RawMessage, error) {
	var userID *uuid.UUID
	var session json.RawMessage
	err := s.q.QueryRow(ctx, `DELETE FROM passkey_challenges WHERE token_hash = $1 AND purpose = $2 AND expires_at > $3
		RETURNING user_id, session`, hash, purpose, now).Scan(&userID, &session)
	return userID, session, mapErr(err)
}

// PrunePasskeyChallenges deletes ceremonies never finished.
func (s *Store) PrunePasskeyChallenges(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM passkey_challenges WHERE expires_at <= $1`, now)
	return int(tag.RowsAffected()), err
}
