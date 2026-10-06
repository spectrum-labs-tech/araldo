// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Password resets by email (ADR 0034).

// CreatePasswordReset stores a reset link's token hash.
func (s *Store) CreatePasswordReset(ctx context.Context, hash []byte, userID uuid.UUID, ip string, expires time.Time) error {
	_, err := s.q.Exec(ctx, `INSERT INTO password_resets (token_hash, user_id, ip, expires_at) VALUES ($1, $2, $3, $4)`, hash, userID, ip, expires)
	return mapErr(err)
}

// CountPasswordResets counts the reset links made for a user since a time.
func (s *Store) CountPasswordResets(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM password_resets WHERE user_id = $1 AND created_at >= $2`, userID, since).Scan(&n)
	return n, err
}

// PasswordResetUser finds whose a reset link is, if it has not expired.
func (s *Store) PasswordResetUser(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	var u uuid.UUID
	err := s.q.QueryRow(ctx, `SELECT user_id FROM password_resets WHERE token_hash = $1 AND expires_at > $2`, hash, now).Scan(&u)
	return u, mapErr(err)
}

// TakePasswordReset uses a reset link, once: it returns whose it was and
// deletes every reset link of theirs.
func (s *Store) TakePasswordReset(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	var u uuid.UUID
	if err := s.q.QueryRow(ctx, `DELETE FROM password_resets WHERE token_hash = $1 AND expires_at > $2 RETURNING user_id`, hash, now).Scan(&u); err != nil {
		return u, mapErr(err)
	}
	_, err := s.q.Exec(ctx, `DELETE FROM password_resets WHERE user_id = $1`, u)
	return u, err
}

// PrunePasswordResets deletes reset links that expired before cutoff.
func (s *Store) PrunePasswordResets(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM password_resets WHERE expires_at < $1`, cutoff)
	return int(tag.RowsAffected()), err
}
