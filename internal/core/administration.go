// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Server administration over the operator API (ADR 0035): what `araldo
// admin` did with the database, done as the operator, with an operator
// key. Only bootstrap, which makes the first key, still opens the database.

// Users.

// OperatorUser finds a user by email, for the operator.
func (s *Service) OperatorUser(ctx context.Context, a Actor, email string) (*model.User, error) {
	if !a.Operator {
		return nil, apperr.Forbidden("Only the server's operator looks people up by email.")
	}
	return s.UserByEmail(ctx, email)
}

// CreateUserAsOperator adds a user with a password, which the operator
// shares with them out of band, and audits it as the operator.
func (s *Service) CreateUserAsOperator(ctx context.Context, a Actor, email, name, password string) (*model.User, error) {
	if !a.Operator {
		return nil, apperr.Forbidden("Only the server's operator creates accounts; people join by invitation.")
	}
	u, norm, err := newUser(email, name, password)
	if err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateUser(ctx, u, norm); err != nil {
			return errEmailTaken(err)
		}
		return s.audit(ctx, tx, a, "user.create", id.Format(id.User, u.ID), nil)
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ResetUserPassword is ResetPassword for the user with an ID.
func (s *Service) ResetUserPassword(ctx context.Context, a Actor, userID uuid.UUID, password string) (*model.User, error) {
	if !a.Operator {
		return nil, apperr.Forbidden("Only the server's operator resets passwords, with araldo admin users reset-password.")
	}
	u, err := s.User(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.ResetPassword(ctx, a, u.Email, password); err != nil {
		return nil, err
	}
	return u, nil
}

// Master keys (ADR 0008).

// MasterKeyUsage reports each master key and how many data keys it wraps.
func (s *Service) MasterKeyUsage(ctx context.Context, a Actor) ([]keyring.KeyUse, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	return s.keys.Usage(ctx)
}

// Rewrap batch sizes: how many data keys one call moves by default, and
// at most, so that no request runs long however big the install.
const (
	DefaultRewrapBatch = 100
	MaxRewrapBatch     = 1000
)

// RewrapDataKeys moves up to batch data keys (DefaultRewrapBatch if 0) to
// the primary master key and says how many are left. Calling it until
// none remain rotates the install; it can be stopped and rerun at any
// point. Each call that moves keys is audited.
func (s *Service) RewrapDataKeys(ctx context.Context, a Actor, batch int) (keyring.Rewrapped, error) {
	if err := requireOperator(a); err != nil {
		return keyring.Rewrapped{}, err
	}
	if batch == 0 {
		batch = DefaultRewrapBatch
	}
	if batch < 1 || batch > MaxRewrapBatch {
		return keyring.Rewrapped{}, apperr.Invalid("batch_size_invalid", "batch_size", "A batch is 1 to %d data keys.", MaxRewrapBatch)
	}
	// Without master keys this is keys_unavailable (503), as every use of
	// the keyring is: retried once they are back.
	r, err := s.keys.Rewrap(ctx, batch)
	if r.Rewrapped > 0 {
		// Recorded even when the batch stopped part way: those keys moved.
		if aerr := s.store.InTx(ctx, func(tx *store.Store) error {
			return s.audit(ctx, tx, a, "master_keys.rewrap", "", map[string]any{"rewrapped": r.Rewrapped, "remaining": r.Remaining})
		}); aerr != nil && err == nil {
			err = aerr
		}
	}
	return r, err
}
