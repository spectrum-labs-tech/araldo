// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Install apps (ADR 0030): developer apps the install registers once, that
// every org can sign in through beside its own. Only the operator manages
// them.

// InstallOperator is the server's operator acting on the install itself,
// in no org, through `araldo admin`.
func InstallOperator(command string) Actor {
	return Actor{Operator: true, Role: model.RoleOwner, OperatorCommand: command, RequestID: OperatorRequestID}
}

func requireOperator(a Actor) error {
	if !a.Operator {
		return apperr.Forbidden("The server's apps are managed by its operator, with araldo admin apps.")
	}
	return nil
}

func installAppSecretAAD(appID uuid.UUID) string {
	return keyring.AAD("install_apps", "client_secret", appID)
}

// checkApp validates and tidies a new app, an org's or the install's.
func (s *Service) checkApp(in *ProviderAppInput) error {
	var ps apperr.Problems
	if _, ok := s.connector(in.Provider); !ok {
		ps.Add("provider_invalid", "provider", "%q does not connect with OAuth.", in.Provider)
	}
	in.Name, in.ClientID, in.ClientSecret = strings.TrimSpace(in.Name), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
	if in.Name == "" {
		in.Name = s.ProviderName(in.Provider) + " app"
	}
	if len(in.Name) > 100 {
		ps.Add("name_invalid", "name", "Names are at most 100 characters.")
	}
	if in.ClientID == "" || len(in.ClientID) > 500 {
		ps.Add("client_id_invalid", "client_id", "Give the app's client ID (app ID).")
	}
	if in.ClientSecret == "" || len(in.ClientSecret) > 500 {
		ps.Add("client_secret_invalid", "client_secret", "Give the app's client secret (app secret).")
	}
	return ps.Err("The app is not valid.")
}

// InstallApps lists the install's apps (never their secrets).
func (s *Service) InstallApps(ctx context.Context, a Actor) ([]*model.ProviderApp, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	return s.store.InstallApps(ctx)
}

// CreateInstallApp registers an app every org can sign in through; its
// secret is sealed with the install's key.
func (s *Service) CreateInstallApp(ctx context.Context, a Actor, in ProviderAppInput) (*model.ProviderApp, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	if err := s.checkApp(&in); err != nil {
		return nil, err
	}
	app := &model.ProviderApp{ID: id.New(), Install: true, Provider: in.Provider, Name: in.Name, ClientID: in.ClientID}
	sealed, err := s.keys.Encrypt(ctx, keyring.Install, installAppSecretAAD(app.ID), []byte(in.ClientSecret))
	if err != nil {
		return nil, err
	}
	app.ClientSecret = sealed
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateInstallApp(ctx, app); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("name_taken", "name", "Another %s app is called %q.", in.Provider, in.Name)
			}
			return err
		}
		return s.audit(ctx, tx, a, "install_app.create", id.Format(id.ProviderApp, app.ID), map[string]any{"provider": in.Provider})
	})
	return app, err
}

// RenameInstallApp changes an install app's name, which orgs see when
// they connect.
func (s *Service) RenameInstallApp(ctx context.Context, a Actor, appID uuid.UUID, name string) (*model.ProviderApp, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	app, err := s.store.InstallApp(ctx, appID)
	if err != nil {
		return nil, notFound(err, "app")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, apperr.Invalid("name_invalid", "name", "An app needs a name of 1 to 100 characters.")
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RenameInstallApp(ctx, appID, name); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("name_taken", "name", "Another %s app is called %q.", app.Provider, name)
			}
			return notFound(err, "app")
		}
		return s.audit(ctx, tx, a, "install_app.rename", id.Format(id.ProviderApp, appID), map[string]any{"name": name})
	})
	app.Name = name
	return app, err
}

// DeleteInstallApp removes an install app. What every org connected
// through it keeps working until its token needs renewing, then needs
// signing in again through another app.
func (s *Service) DeleteInstallApp(ctx context.Context, a Actor, appID uuid.UUID) error {
	if err := requireOperator(a); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteInstallApp(ctx, appID); err != nil {
			return notFound(err, "app")
		}
		return s.audit(ctx, tx, a, "install_app.delete", id.Format(id.ProviderApp, appID), nil)
	})
}

// appFor loads the app a channel, ad account or sign-in names: the org's,
// or the install's.
func appFor(ctx context.Context, st *store.Store, orgID, appID uuid.UUID, install bool) (*model.ProviderApp, error) {
	if install {
		return st.InstallApp(ctx, appID)
	}
	return st.ProviderApp(ctx, orgID, appID)
}

// connectApp finds the app a member chose to sign in through: one of the
// org's, or else one of the install's.
func (s *Service) connectApp(ctx context.Context, orgID, appID uuid.UUID) (*model.ProviderApp, error) {
	app, err := s.store.ProviderApp(ctx, orgID, appID)
	if errors.Is(err, store.ErrNotFound) {
		app, err = s.store.InstallApp(ctx, appID)
	}
	return app, err
}
