// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Mail connections (ADR 0024 decision 3, amended): one email provider
// account's credentials, which any number of senders (mail accounts) use.
// A connection belongs to the org; a key limited to one brand sees and
// uses only the connections its brand's senders use, and cannot change
// any, since they serve other brands too.

func mailConnectionAAD(connectionID uuid.UUID) string {
	return keyring.AAD("mail_connections", "credentials", connectionID)
}

// connectionAAD is what c's credentials are sealed with: its own, or,
// until its key is next replaced, the sender's it was adopted from (whose
// ID it has).
func connectionAAD(c *model.MailConnection) string {
	if c.LegacyAAD {
		return mailCredentialsAAD(c.ID)
	}
	return mailConnectionAAD(c.ID)
}

const maxConnectionName = 100

// MailConnectionInput connects a provider account.
type MailConnectionInput struct {
	Provider email.Provider
	// Name defaults to the provider's name.
	Name   string
	Fields map[string]string
}

// MailConnectionUpdate renames a connection or replaces its credentials;
// a nil field is left as it is.
type MailConnectionUpdate struct {
	Name   *string
	Fields map[string]string
}

func checkConnectionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxConnectionName {
		return "", apperr.Invalid("name_invalid", "name", "A connection needs a name of 1 to %d characters.", maxConnectionName)
	}
	return name, nil
}

// mailerOf checks a provider is one the actor's mode can use and returns
// it.
func (s *Service) mailerOf(a Actor, p email.Provider) (email.Mailer, error) {
	switch {
	case !a.Livemode && p != email.Sandbox:
		return nil, apperr.Invalid("livemode_required", "provider", "Test mode can only connect sandbox mail accounts.")
	case a.Livemode && p == email.Sandbox:
		return nil, apperr.Invalid("testmode_required", "provider", "Sandbox mail accounts exist only in test mode.")
	}
	m, ok := s.mailers.Get(p)
	if !ok {
		return nil, apperr.Invalid("provider_unsupported", "provider", "This install cannot send newsletters through %q.", p)
	}
	return m, nil
}

// checkConnectionKey checks credentials work by reading the provider's
// audiences: a connection has no sender to verify yet, and every
// provider lists audiences with the key a connection needs.
func checkConnectionKey(ctx context.Context, m email.Mailer, creds platform.Credentials) error {
	if _, err := callProvider(ctx, func(ctx context.Context) ([]email.Audience, error) { return m.Audiences(ctx, creds) }); err != nil {
		return connectError(platform.Provider(m.Name()), err)
	}
	return nil
}

// CreateMailConnection checks a provider account's credentials and stores
// them, for senders to use.
func (s *Service) CreateMailConnection(ctx context.Context, a Actor, in MailConnectionInput) (*model.MailConnection, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		return nil, errBrandKeyConnections
	}
	m, err := s.mailerOf(a, in.Provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = m.Name()
	}
	name, err := checkConnectionName(in.Name)
	if err != nil {
		return nil, err
	}
	settings, secrets, err := splitFields(m.Name()+" connections", m.Fields(), in.Fields)
	if err != nil {
		return nil, err
	}
	if err := checkConnectionKey(ctx, m, mergeCredentials(settings, secrets)); err != nil {
		return nil, err
	}
	c := &model.MailConnection{ID: id.New(), OrgID: a.OrgID, Livemode: a.Livemode, Provider: string(in.Provider), Name: name,
		Settings: settings, Status: model.ConnectionActive}
	if c.Credentials, err = sealSecrets(ctx, s.keys, a.OrgID, mailConnectionAAD(c.ID), secrets); err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateMailConnection(ctx, c); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "mail_connection.create", id.Format(id.MailConnection, c.ID), map[string]any{"provider": in.Provider})
	})
	return c, err
}

var errBrandKeyConnections = apperr.Forbidden("A key limited to one brand cannot change mail connections, which other brands may use.")

// MailConnections lists the actor's mail connections, with how many
// senders use each.
func (s *Service) MailConnections(ctx context.Context, a Actor) ([]*model.MailConnection, error) {
	if err := a.requireAny(PermChannelsRead, PermNewslettersRead); err != nil {
		return nil, err
	}
	if err := s.store.AdoptMailAccounts(ctx, a.OrgID, a.Livemode, nil); err != nil {
		return nil, err
	}
	all, err := s.store.MailConnections(ctx, a.OrgID, a.Livemode)
	if err != nil || a.BrandID == nil {
		return all, err
	}
	var out []*model.MailConnection
	for _, c := range all {
		if ok, err := s.servesBrand(ctx, c, *a.BrandID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// servesBrand reports whether one of brandID's senders uses c.
func (s *Service) servesBrand(ctx context.Context, c *model.MailConnection, brandID uuid.UUID) (bool, error) {
	senders, err := s.store.ConnectionSenders(ctx, c.OrgID, c.ID)
	if err != nil {
		return false, err
	}
	for _, m := range senders {
		if m.BrandID == brandID {
			return true, nil
		}
	}
	return false, nil
}

// MailConnection returns one of the actor's mail connections.
func (s *Service) MailConnection(ctx context.Context, a Actor, connectionID uuid.UUID) (*model.MailConnection, error) {
	if err := a.requireAny(PermChannelsRead, PermNewslettersRead); err != nil {
		return nil, err
	}
	c, err := s.store.MailConnection(ctx, a.OrgID, connectionID)
	if err != nil {
		return nil, notFound(err, "mail connection")
	}
	if c.Livemode != a.Livemode {
		return nil, apperr.NotFound("mail connection")
	}
	if a.BrandID != nil {
		if ok, err := s.servesBrand(ctx, c, *a.BrandID); err != nil {
			return nil, err
		} else if !ok {
			return nil, apperr.NotFound("mail connection")
		}
	}
	return c, nil
}

// MailConnectionSenders lists the senders that use one of the actor's
// connections (for a key limited to one brand, its brand's).
func (s *Service) MailConnectionSenders(ctx context.Context, a Actor, connectionID uuid.UUID) ([]*model.MailAccount, error) {
	c, err := s.MailConnection(ctx, a, connectionID)
	if err != nil {
		return nil, err
	}
	all, err := s.store.ConnectionSenders(ctx, a.OrgID, c.ID)
	if err != nil || a.BrandID == nil {
		return all, err
	}
	var out []*model.MailAccount
	for _, m := range all {
		if m.BrandID == *a.BrandID {
			out = append(out, m)
		}
	}
	return out, nil
}

// UpdateMailConnection renames a connection, or replaces its credentials
// (checked with the provider first) on it and on every sender's copy, and
// marks it and its senders usable again.
func (s *Service) UpdateMailConnection(ctx context.Context, a Actor, connectionID uuid.UUID, in MailConnectionUpdate) (*model.MailConnection, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		return nil, errBrandKeyConnections
	}
	c, err := s.MailConnection(ctx, a, connectionID)
	if err != nil {
		return nil, err
	}
	var name string
	if in.Name != nil {
		if name, err = checkConnectionName(*in.Name); err != nil {
			return nil, err
		}
	}
	var settings, secrets map[string]string
	var sealed []byte
	if in.Fields != nil {
		m, ok := s.mailers.Get(email.Provider(c.Provider))
		if !ok {
			return nil, apperr.Conflict("provider_unsupported", "This install cannot send through %s.", c.Provider)
		}
		if settings, secrets, err = splitFields(m.Name()+" connections", m.Fields(), in.Fields); err != nil {
			return nil, err
		}
		if err := checkConnectionKey(ctx, m, mergeCredentials(settings, secrets)); err != nil {
			return nil, err
		}
		if sealed, err = sealSecrets(ctx, s.keys, a.OrgID, mailConnectionAAD(c.ID), secrets); err != nil {
			return nil, err
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if in.Name != nil {
			if err := tx.RenameMailConnection(ctx, a.OrgID, c.ID, name); err != nil {
				return err
			}
		}
		if in.Fields != nil {
			if err := tx.SetMailConnectionCredentials(ctx, a.OrgID, c.ID, settings, sealed); err != nil {
				return err
			}
			// The release before connections reads each sender's copy
			// (ADR 0024, "Connections and senders").
			senders, err := tx.ConnectionSenders(ctx, a.OrgID, c.ID)
			if err != nil {
				return err
			}
			for _, ma := range senders {
				copySealed, err := sealSecrets(ctx, s.keys.With(tx), a.OrgID, mailCredentialsAAD(ma.ID), secrets)
				if err != nil {
					return err
				}
				if err := tx.SetMailAccountCredentials(ctx, a.OrgID, ma.ID, settings, copySealed); err != nil {
					return err
				}
			}
		}
		meta := map[string]any{"renamed": in.Name != nil, "credentials_replaced": in.Fields != nil}
		return s.audit(ctx, tx, a, "mail_connection.update", id.Format(id.MailConnection, c.ID), meta)
	})
	if err != nil {
		return nil, err
	}
	return s.store.MailConnection(ctx, a.OrgID, c.ID)
}

// DeleteMailConnection forgets a connection and its credentials. One a
// sender uses is refused: delete or move its senders first.
func (s *Service) DeleteMailConnection(ctx context.Context, a Actor, connectionID uuid.UUID) error {
	if err := a.require(PermChannelsWrite); err != nil {
		return err
	}
	if a.BrandID != nil {
		return errBrandKeyConnections
	}
	c, err := s.MailConnection(ctx, a, connectionID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteMailConnection(ctx, a.OrgID, c.ID); err != nil {
			if errors.Is(err, store.ErrReferenced) {
				return apperr.Conflict("mail_connection_in_use", "Senders use this connection; delete them first.")
			}
			return err
		}
		return s.audit(ctx, tx, a, "mail_connection.delete", id.Format(id.MailConnection, c.ID), map[string]any{"provider": c.Provider})
	})
}

// connectionOf returns the connection a sender uses, adopting a sender
// connected before connections existed first.
func (s *Service) connectionOf(ctx context.Context, ma *model.MailAccount) (*model.MailConnection, error) {
	if ma.ConnectionID == nil {
		if err := s.adopt(ctx, ma); err != nil {
			return nil, err
		}
	}
	return s.store.MailConnection(ctx, ma.OrgID, *ma.ConnectionID)
}

// adopt gives a sender without a connection one of its own, and sets
// ma.ConnectionID.
func (s *Service) adopt(ctx context.Context, ma *model.MailAccount) error {
	if err := s.store.AdoptMailAccounts(ctx, ma.OrgID, ma.Livemode, &ma.ID); err != nil {
		return err
	}
	fresh, err := s.store.MailAccount(ctx, ma.OrgID, ma.ID)
	if err != nil {
		return err
	}
	if fresh.ConnectionID == nil {
		return errors.New("core: adopting a mail account left it without a connection")
	}
	ma.ConnectionID = fresh.ConnectionID
	return nil
}

// connectionCredentials opens a connection's credentials.
func (s *Service) connectionCredentials(ctx context.Context, c *model.MailConnection) (platform.Credentials, error) {
	return openCredentials(ctx, s.keys, c.OrgID, connectionAAD(c), c.Settings, c.Credentials)
}

// MailConnectionAudiences lists what a connection's provider account can
// send to; every sender on it offers the same.
func (s *Service) MailConnectionAudiences(ctx context.Context, a Actor, connectionID uuid.UUID) ([]email.Audience, error) {
	c, err := s.MailConnection(ctx, a, connectionID)
	if err != nil {
		return nil, err
	}
	m, ok := s.mailers.Get(email.Provider(c.Provider))
	if !ok {
		return nil, apperr.Conflict("provider_unsupported", "This install cannot send through %s.", c.Provider)
	}
	creds, err := s.connectionCredentials(ctx, c)
	if err != nil {
		return nil, err
	}
	out, err := m.Audiences(ctx, creds)
	if err != nil {
		if platform.KindOf(err) == platform.AuthRevoked {
			note := truncate("The provider refused the key: replace it on the mail connection. "+err.Error(), 500)
			if serr := s.store.SetMailConnectionStatus(ctx, c.OrgID, c.ID, model.ConnectionNeedsReauth, note); serr != nil {
				s.log.WarnContext(ctx, "flagging a mail connection failed", "mail_connection", id.Format(id.MailConnection, c.ID), "err", serr)
			}
		}
		return nil, connectError(platform.Provider(m.Name()), err)
	}
	return out, nil
}
