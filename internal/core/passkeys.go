// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Passkeys (ADR 0007): WebAuthn credentials that sign in alone, as
// multi-factor (the device verifies the person), and serve as a second
// factor after a password and to confirm one.

// PasskeyChallengeTTL is how long a ceremony may take between its halves.
const PasskeyChallengeTTL = 5 * time.Minute

const (
	ceremonyRegister = "register"
	ceremonyLogin    = "login"
	ceremonyAssert   = "assert"
)

var errPasskeyInvalid = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "passkey_invalid",
	Message: "That passkey was not accepted. Try again, or use another way to sign in."}

// relyingParty is the WebAuthn relying party: this server, by the host of
// its base URL. Passkeys are bound to that host; changing it leaves them
// unusable.
func (s *Service) relyingParty() (*webauthn.WebAuthn, error) {
	u, err := url.Parse(s.cfg.BaseURL)
	if err != nil || u.Hostname() == "" {
		return nil, apperr.Invalid("base_url_invalid", "", "Passkeys need the server's base URL.")
	}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Araldo", RPOrigins: []string{u.Scheme + "://" + u.Host}})
}

// passkeyUser is a user as the WebAuthn library sees one: their handle is
// their user ID.
type passkeyUser struct {
	u     *model.User
	creds []webauthn.Credential
}

func (p passkeyUser) WebAuthnID() []byte   { return p.u.ID[:] }
func (p passkeyUser) WebAuthnName() string { return p.u.Email }
func (p passkeyUser) WebAuthnDisplayName() string {
	if p.u.Name != "" {
		return p.u.Name
	}
	return p.u.Email
}
func (p passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

func (s *Service) passkeyUser(ctx context.Context, userID uuid.UUID) (passkeyUser, error) {
	u, err := s.store.User(ctx, userID)
	if err != nil {
		return passkeyUser{}, err
	}
	raws, err := s.store.PasskeyCredentials(ctx, userID)
	if err != nil {
		return passkeyUser{}, err
	}
	p := passkeyUser{u: u}
	for _, raw := range raws {
		var c webauthn.Credential
		if err := json.Unmarshal(raw, &c); err != nil {
			return passkeyUser{}, err
		}
		p.creds = append(p.creds, c)
	}
	return p, nil
}

// saveChallenge keeps a ceremony's state and returns its token.
func (s *Service) saveChallenge(ctx context.Context, userID *uuid.UUID, purpose string, sd *webauthn.SessionData) (string, error) {
	raw, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	token, hash := authn.NewToken()
	return token, s.store.CreatePasskeyChallenge(ctx, hash, userID, purpose, raw, s.Now().Add(PasskeyChallengeTTL))
}

// takeChallenge returns a ceremony's state, once.
func (s *Service) takeChallenge(ctx context.Context, token, purpose string) (*uuid.UUID, webauthn.SessionData, error) {
	var sd webauthn.SessionData
	userID, raw, err := s.store.TakePasskeyChallenge(ctx, authn.HashToken(token), purpose, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, sd, &apperr.Error{Kind: apperr.KindUnauthorized, Code: "passkey_expired", Message: "That took too long. Try again."}
	}
	if err != nil {
		return nil, sd, err
	}
	return userID, sd, json.Unmarshal(raw, &sd)
}

// BeginPasskeyRegistration starts adding a passkey to the session's user,
// after a recent password confirmation. It returns the options for the
// browser (navigator.credentials.create) and the ceremony's token.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, ss *model.Session) (json.RawMessage, string, error) {
	if err := s.requireSudo(ss); err != nil {
		return nil, "", err
	}
	rp, err := s.relyingParty()
	if err != nil {
		return nil, "", err
	}
	pu, err := s.passkeyUser(ctx, ss.UserID)
	if err != nil {
		return nil, "", err
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range pu.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, sd, err := rp.BeginRegistration(pu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(), UserVerification: protocol.VerificationRequired}),
		webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, "", err
	}
	token, err := s.saveChallenge(ctx, &ss.UserID, ceremonyRegister, sd)
	if err != nil {
		return nil, "", err
	}
	options, err := json.Marshal(creation)
	return options, token, err
}

// FinishPasskeyRegistration checks the browser's new credential and keeps
// it as a passkey named name.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, ss *model.Session, token, name string, response []byte) (*model.Passkey, error) {
	if err := s.requireSudo(ss); err != nil {
		return nil, err
	}
	userID, sd, err := s.takeChallenge(ctx, token, ceremonyRegister)
	if err != nil {
		return nil, err
	}
	if userID == nil || *userID != ss.UserID {
		return nil, errPasskeyInvalid
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if len([]rune(name)) > 100 {
		return nil, apperr.Invalid("name_invalid", "name", "A passkey's name is at most 100 characters.")
	}
	rp, err := s.relyingParty()
	if err != nil {
		return nil, err
	}
	pu, err := s.passkeyUser(ctx, ss.UserID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, errPasskeyInvalid
	}
	cred, err := rp.CreateCredential(pu, sd, parsed)
	if err != nil {
		return nil, errPasskeyInvalid
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	p := &model.Passkey{ID: id.New(), UserID: ss.UserID, Name: name, CreatedAt: s.Now()}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreatePasskey(ctx, p, cred.ID, raw); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("passkey_exists", "That passkey is already added.")
			}
			return err
		}
		return s.audit(ctx, tx, Actor{UserID: &ss.UserID, RequestID: "dashboard"}, "passkey.add", p.ID.String(), map[string]any{"name": name})
	})
	return p, err
}

// BeginPasskeyLogin starts signing in with a passkey, for anyone: the
// browser offers the passkeys it holds for this server.
func (s *Service) BeginPasskeyLogin(ctx context.Context) (json.RawMessage, string, error) {
	rp, err := s.relyingParty()
	if err != nil {
		return nil, "", err
	}
	assertion, sd, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	token, err := s.saveChallenge(ctx, nil, ceremonyLogin, sd)
	if err != nil {
		return nil, "", err
	}
	options, err := json.Marshal(assertion)
	return options, token, err
}

// FinishPasskeyLogin checks the browser's answer and signs its user in: a
// passkey verifies the person, so the session needs no second factor and
// starts in sudo mode.
func (s *Service) FinishPasskeyLogin(ctx context.Context, token string, response []byte, userAgent, ip string) (*LoginResult, error) {
	_, sd, err := s.takeChallenge(ctx, token, ceremonyLogin)
	if err != nil {
		return nil, err
	}
	rp, err := s.relyingParty()
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, errPasskeyInvalid
	}
	var who passkeyUser
	cred, err := rp.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		userID, err := uuid.FromBytes(userHandle)
		if err != nil {
			return nil, err
		}
		owner, err := s.store.PasskeyOwner(ctx, rawID)
		if err != nil || owner != userID {
			return nil, errPasskeyInvalid
		}
		if who, err = s.passkeyUser(ctx, userID); err != nil {
			return nil, err
		}
		return who, nil
	}, sd, parsed)
	if err != nil || who.u == nil {
		return nil, errPasskeyInvalid
	}
	now := s.Now()
	if err := locked(who.u, now); err != nil {
		return nil, err
	}
	if err := s.usePasskey(ctx, who.u.ID, cred); err != nil {
		return nil, err
	}
	if err := s.resetFailures(ctx, who.u); err != nil {
		return nil, err
	}
	return s.startSession(ctx, who.u, userAgent, ip, now, true)
}

func (s *Service) usePasskey(ctx context.Context, userID uuid.UUID, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return s.store.UsePasskey(ctx, userID, cred.ID, raw, s.Now())
}

// BeginPasskeyAssertion starts using one of the session's user's passkeys:
// as the second factor of a password sign-in, or to confirm it is them.
func (s *Service) BeginPasskeyAssertion(ctx context.Context, ss *model.Session) (json.RawMessage, string, error) {
	rp, err := s.relyingParty()
	if err != nil {
		return nil, "", err
	}
	pu, err := s.passkeyUser(ctx, ss.UserID)
	if err != nil {
		return nil, "", err
	}
	if len(pu.creds) == 0 {
		return nil, "", apperr.Invalid("passkey_missing", "", "You have no passkey on this account.")
	}
	assertion, sd, err := rp.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	token, err := s.saveChallenge(ctx, &ss.UserID, ceremonyAssert, sd)
	if err != nil {
		return nil, "", err
	}
	options, err := json.Marshal(assertion)
	return options, token, err
}

// FinishPasskeyAssertion checks the passkey's answer: a session waiting
// for its second factor becomes active, and an active one enters sudo
// mode, as a password confirmation would.
func (s *Service) FinishPasskeyAssertion(ctx context.Context, ss *model.Session, token string, response []byte) error {
	userID, sd, err := s.takeChallenge(ctx, token, ceremonyAssert)
	if err != nil {
		return err
	}
	if userID == nil || *userID != ss.UserID {
		return errPasskeyInvalid
	}
	rp, err := s.relyingParty()
	if err != nil {
		return err
	}
	pu, err := s.passkeyUser(ctx, ss.UserID)
	if err != nil {
		return err
	}
	if err := locked(pu.u, s.Now()); err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return errPasskeyInvalid
	}
	cred, err := rp.ValidateLogin(pu, sd, parsed)
	if err != nil {
		return errPasskeyInvalid
	}
	if err := s.usePasskey(ctx, ss.UserID, cred); err != nil {
		return err
	}
	if err := s.resetFailures(ctx, pu.u); err != nil {
		return err
	}
	if ss.State == model.SessionPendingMFA {
		return s.store.ActivateSession(ctx, ss.ID, s.Now().Add(SudoWindow))
	}
	return s.store.SetSessionSudo(ctx, ss.ID, s.Now().Add(SudoWindow))
}

// Passkeys lists a user's passkeys.
func (s *Service) Passkeys(ctx context.Context, userID uuid.UUID) ([]*model.Passkey, error) {
	return s.store.Passkeys(ctx, userID)
}

// DeletePasskey removes one of the session's user's passkeys, after a
// recent confirmation; not their last second factor while an org they
// belong to requires one.
func (s *Service) DeletePasskey(ctx context.Context, ss *model.Session, passkeyID uuid.UUID) error {
	if err := s.requireSudo(ss); err != nil {
		return err
	}
	u, err := s.store.User(ctx, ss.UserID)
	if err != nil {
		return err
	}
	keys, err := s.store.Passkeys(ctx, ss.UserID)
	if err != nil {
		return err
	}
	if len(keys) == 1 && keys[0].ID == passkeyID && !u.MFAEnabled() {
		if org, err := s.orgRequiringMFA(ctx, ss.UserID); err != nil || org != "" {
			if err != nil {
				return err
			}
			return apperr.Forbidden("%s requires two-factor authentication: add another passkey or an authenticator app first.", org)
		}
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeletePasskey(ctx, ss.UserID, passkeyID); err != nil {
			return notFound(err, "passkey")
		}
		return s.audit(ctx, tx, Actor{UserID: &ss.UserID, RequestID: "dashboard"}, "passkey.remove", passkeyID.String(), nil)
	})
}

// orgRequiringMFA names an org of the user's that requires two-factor
// authentication, or "" if none does.
func (s *Service) orgRequiringMFA(ctx context.Context, userID uuid.UUID) (string, error) {
	ms, err := s.store.UserMemberships(ctx, userID)
	if err != nil {
		return "", err
	}
	for _, m := range ms {
		o, err := s.store.Org(ctx, m.OrgID)
		if err != nil {
			return "", err
		}
		if o.RequireMFA {
			return o.Name, nil
		}
	}
	return "", nil
}
