// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Device sign-in and user tokens (ADR 0028): the CLI asks for a device
// code, the person approves its user code in the dashboard, signed in, and
// the CLI receives a token that acts as them, in one mode, in whichever of
// their orgs each request names.

const (
	// DeviceCodeTTL is how long a device sign-in waits for approval.
	DeviceCodeTTL = 15 * time.Minute
	// devicePollInterval is how often, in seconds, the CLI may poll; one
	// that polls faster is told to slow down by five seconds more.
	devicePollInterval = 5
	// UserTokenIdle is how long a token lasts unused (as GitHub's for gh).
	UserTokenIdle = 365 * 24 * time.Hour
)

// DeviceStart is what the CLI gets to start a sign-in (RFC 8628 §3.2). It
// has no verification_uri_complete, the RFC's optional link with the code
// in it: the person always types the code, which is what proves the
// request is their own (RFC 8628 §5.4), so a link cannot be sent to them.
type DeviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartDevice begins a device sign-in for a token in live mode or test mode.
func (s *Service) StartDevice(ctx context.Context, deviceName string, livemode bool, ip string) (*DeviceStart, error) {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		deviceName = "araldo CLI"
	}
	if len(deviceName) > 100 {
		return nil, apperr.Invalid("device_name_invalid", "device_name", "A device name is at most 100 characters.")
	}
	code, hash := authn.NewToken()
	d := &model.DeviceAuthorization{ID: id.New(), DeviceName: deviceName, Livemode: livemode, ClientIP: truncate(ip, 64),
		PollInterval: devicePollInterval, ExpiresAt: s.Now().Add(DeviceCodeTTL)}
	var err error
	for range 5 { // a user code already waiting is drawn again
		d.UserCode = authn.NewUserCode()
		if err = s.store.CreateDeviceAuthorization(ctx, d, hash); !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return &DeviceStart{DeviceCode: code, UserCode: d.UserCode, VerificationURI: s.cfg.BaseURL + "/device",
		ExpiresIn: int(DeviceCodeTTL / time.Second), Interval: devicePollInterval}, nil
}

var errNoDevice = &apperr.Error{Kind: apperr.KindNotFound, Code: "device_code_unknown",
	Message: "No sign-in is waiting with that code. Check it, or run araldo auth login again for a new one."}

// PendingDevice finds the sign-in waiting with a user code, as typed.
func (s *Service) PendingDevice(ctx context.Context, userCode string) (*model.DeviceAuthorization, error) {
	code := authn.NormalizeUserCode(userCode)
	if code == "" {
		return nil, errNoDevice
	}
	d, err := s.store.PendingDeviceByCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) || err == nil && !s.Now().Before(d.ExpiresAt) {
		return nil, errNoDevice
	}
	return d, err
}

// DecideDevice approves or denies a waiting sign-in as the signed-in user.
// Approving creates a credential, so it needs sudo mode (ADR 0007).
func (s *Service) DecideDevice(ctx context.Context, ss *model.Session, userCode string, approve bool) (*model.DeviceAuthorization, error) {
	if approve {
		if err := s.requireSudo(ss); err != nil {
			return nil, err
		}
	}
	d, err := s.PendingDevice(ctx, userCode)
	if err != nil {
		return nil, err
	}
	status, action := "denied", "user_token.deny"
	if approve {
		status, action = "approved", "user_token.approve"
	}
	a := Actor{UserID: &ss.UserID, RequestID: "dashboard"}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DecideDevice(ctx, d.ID, status, ss.UserID, ss.SSOOrg); err != nil {
			return notFoundAs(err, errNoDevice)
		}
		return s.audit(ctx, tx, a, action, id.Format(id.Device, d.ID),
			map[string]any{"device_name": d.DeviceName, "livemode": d.Livemode, "client_ip": d.ClientIP})
	})
	return d, err
}

// Device sign-in answers while the CLI polls (RFC 8628 §3.5), as problems
// with the RFC's error codes.
var (
	errDevicePending  = &apperr.Error{Kind: apperr.KindBadRequest, Code: "authorization_pending", Message: "Waiting for approval in the dashboard."}
	errDeviceSlowDown = &apperr.Error{Kind: apperr.KindBadRequest, Code: "slow_down", Message: "Polling too often: wait longer between requests."}
	errDeviceDenied   = &apperr.Error{Kind: apperr.KindBadRequest, Code: "access_denied", Message: "The sign-in was denied."}
	errDeviceExpired  = &apperr.Error{Kind: apperr.KindBadRequest, Code: "expired_token", Message: "The code expired: start again."}
	errDeviceInvalid  = &apperr.Error{Kind: apperr.KindBadRequest, Code: "invalid_grant", Message: "That device code is unknown or was used."}
)

// PollDevice is the CLI asking whether its sign-in was approved. Once it
// was, it returns the token, once.
func (s *Service) PollDevice(ctx context.Context, deviceCode string) (string, *model.UserToken, error) {
	now := s.Now()
	var answer error
	var plain string
	var tok *model.UserToken
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		d, err := tx.PollDevice(ctx, authn.HashToken(deviceCode), now)
		switch {
		case errors.Is(err, store.ErrNotFound):
			answer = errDeviceInvalid
			return nil
		case err != nil:
			return err
		case !now.Before(d.ExpiresAt):
			answer = errDeviceExpired
			return nil
		case d.LastPolledAt != nil && now.Sub(*d.LastPolledAt) < time.Duration(d.PollInterval)*time.Second:
			answer = errDeviceSlowDown
			return tx.SlowDevice(ctx, d.ID, d.PollInterval+5)
		}
		switch d.Status {
		case "pending":
			answer = errDevicePending
			return nil
		case "denied":
			answer = errDeviceDenied
			return nil
		case "issued":
			answer = errDeviceInvalid
			return nil
		}
		if err := tx.IssueDevice(ctx, d.ID); err != nil {
			return err
		}
		plain = authn.NewUserToken()
		tok = &model.UserToken{ID: id.New(), UserID: *d.UserID, Livemode: d.Livemode, Name: d.DeviceName, Hint: authn.KeyHint(plain),
			CreatedIP: d.ClientIP, CreatedAt: now, SSOOrg: d.SSOOrg}
		if err := tx.CreateUserToken(ctx, tok, authn.HashToken(plain)); err != nil {
			return err
		}
		return s.audit(ctx, tx, Actor{UserID: d.UserID, RequestID: "device"}, "user_token.create", id.Format(id.UserToken, tok.ID),
			map[string]any{"device_name": d.DeviceName, "livemode": d.Livemode})
	})
	if err != nil {
		return "", nil, err
	}
	if answer != nil {
		return "", nil, answer
	}
	return plain, tok, nil
}

var errTokenInvalid = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "user_token_invalid",
	Message: "This token was revoked, has not been used in a year, or does not exist: run araldo auth login."}

// AuthenticateUserToken turns a user token into an actor: the person, as a
// member of the org named (an ID or name; empty when they belong to one),
// in the token's mode. Their role is read now, so a change takes effect at
// once; an org that requires two-factor authentication refuses a person
// without it, and one that requires single sign-on a token not approved
// from a session that came through it (ADR 0033).
func (s *Service) AuthenticateUserToken(ctx context.Context, token, org, requestID string) (Actor, error) {
	if !authn.IsUserToken(token) {
		return Actor{}, errTokenInvalid
	}
	t, err := s.store.UserTokenByHash(ctx, authn.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, errTokenInvalid
	}
	if err != nil {
		return Actor{}, err
	}
	now := s.Now()
	last := t.CreatedAt
	if t.LastUsedAt != nil {
		last = *t.LastUsedAt
	}
	if t.RevokedAt != nil || now.Sub(last) > UserTokenIdle {
		return Actor{}, errTokenInvalid
	}
	ms, err := s.store.UserMemberships(ctx, t.UserID)
	if err != nil {
		return Actor{}, err
	}
	m, err := pickMembership(ms, org)
	if err != nil {
		return Actor{}, err
	}
	o, err := s.store.Org(ctx, m.OrgID)
	if err != nil {
		return Actor{}, err
	}
	if o.RequireMFA {
		u, err := s.store.User(ctx, t.UserID)
		if err != nil {
			return Actor{}, err
		}
		if !u.SecondFactor() {
			return Actor{}, apperr.Forbidden("%s requires two-factor authentication: turn it on in the dashboard, under your account.", o.Name)
		}
	}
	if o.RequireSSO && (t.SSOOrg == nil || *t.SSOOrg != o.ID) {
		return Actor{}, &apperr.Error{Kind: apperr.KindForbidden, Code: "sso_required",
			Message: o.Name + " requires single sign-on: run araldo auth login again, and approve it signed in through single sign-on."}
	}
	if o.Status == model.OrgSuspended {
		return Actor{}, errOrgSuspended
	}
	_ = s.store.TouchUserToken(ctx, t.ID, now)
	return Actor{OrgID: m.OrgID, Livemode: t.Livemode, UserID: &t.UserID, Role: m.Role, TokenID: &t.ID, RequestID: requestID,
		OrgStatus: o.Status}, nil
}

// pickMembership chooses the membership an org header names: by ID or
// name, or the only one.
func pickMembership(ms []model.Membership, org string) (model.Membership, error) {
	org = strings.TrimSpace(org)
	if org == "" {
		if len(ms) == 1 {
			return ms[0], nil
		}
		names := make([]string, len(ms))
		for i, m := range ms {
			names[i] = m.OrgName
		}
		return model.Membership{}, apperr.Invalid("org_required", "Araldo-Org",
			"You belong to %d orgs: name one in the Araldo-Org header (%s).", len(ms), strings.Join(names, ", "))
	}
	if u, err := id.Parse(id.Org, org); err == nil {
		for _, m := range ms {
			if m.OrgID == u {
				return m, nil
			}
		}
	}
	var found []model.Membership
	for _, m := range ms {
		if strings.EqualFold(m.OrgName, org) {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return model.Membership{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "org_unknown", Param: "Araldo-Org",
			Message: "You do not belong to an org " + org + "."}
	}
	return model.Membership{}, apperr.Invalid("org_ambiguous", "Araldo-Org", "You belong to more than one org named %q: give its ID.", org)
}

// UserTokens lists a person's tokens (the dashboard's Devices).
func (s *Service) UserTokens(ctx context.Context, userID uuid.UUID) ([]*model.UserToken, error) {
	return s.store.UserTokens(ctx, userID)
}

// RevokeUserToken stops one of a person's tokens working.
func (s *Service) RevokeUserToken(ctx context.Context, userID, tokenID uuid.UUID) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RevokeUserToken(ctx, userID, tokenID, s.Now()); err != nil {
			return notFound(err, "token")
		}
		return s.audit(ctx, tx, Actor{UserID: &userID}, "user_token.revoke", id.Format(id.UserToken, tokenID), nil)
	})
}

// RevokeOwnToken is a token revoking itself (araldo auth logout).
func (s *Service) RevokeOwnToken(ctx context.Context, a Actor) error {
	if a.TokenID == nil || a.UserID == nil {
		return apperr.Forbidden("Only a user token can sign itself out; revoke an API key in the dashboard.")
	}
	return s.RevokeUserToken(ctx, *a.UserID, *a.TokenID)
}
