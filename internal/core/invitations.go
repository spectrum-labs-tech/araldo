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

// Invitations (ADR 0004): a person joins an org by accepting a one-time
// link an admin shares. Inviting says nothing about whether the email has
// an account, and nobody is added to an org without accepting.

// InvitationTTL is how long an invitation link works.
const InvitationTTL = 7 * 24 * time.Hour

// errInvitationInvalid is the one answer for an unknown, used or expired
// link.
var errInvitationInvalid = &apperr.Error{Kind: apperr.KindNotFound, Code: "invitation_invalid",
	Message: "This invitation link is not valid: it was used, withdrawn or has expired. Ask for a new one."}

// InviteMember invites email to the org with role, and returns the link to
// share. Inviting an owner needs what making one does: an owner, in sudo
// mode. Inviting the same person again replaces the open invitation.
func (s *Service) InviteMember(ctx context.Context, a Actor, ss *model.Session, email string, role model.Role) (string, *model.Invitation, error) {
	if err := a.require(PermMembersWrite); err != nil {
		return "", nil, err
	}
	if !role.Valid() {
		return "", nil, apperr.Invalid("role_invalid", "role", "Role must be owner, admin, editor or viewer.")
	}
	if role == model.RoleOwner {
		if !a.Can(PermOrgWrite) {
			return "", nil, apperr.Forbidden("Only owners can invite owners.")
		}
		if err := s.requireSudoFor(a, ss); err != nil {
			return "", nil, err
		}
	}
	norm, err := NormalizeEmail(email)
	if err != nil {
		return "", nil, err
	}
	if err := s.checkLimit(ctx, a, limitMembers, 1, norm); err != nil {
		return "", nil, err
	}
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return "", nil, err
	}
	var link string
	var inv *model.Invitation
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		link, inv, err = s.newInvitation(ctx, tx, a, o.Name, email, norm, role)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return link, inv, nil
}

// newInvitation records an invitation to the actor's org, in tx, and
// returns its link.
func (s *Service) newInvitation(ctx context.Context, tx *store.Store, a Actor, orgName, email, norm string, role model.Role) (string, *model.Invitation, error) {
	token, hash := authn.NewToken()
	inv := &model.Invitation{ID: id.New(), OrgID: a.OrgID, OrgName: orgName, Email: strings.TrimSpace(email), Role: role,
		InvitedBy: a.UserID, ExpiresAt: s.Now().Add(InvitationTTL), CreatedAt: s.Now()}
	if err := tx.CreateInvitation(ctx, inv, norm, hash); err != nil {
		return "", nil, err
	}
	if err := s.audit(ctx, tx, a, "member.invite", id.Format(id.Invitation, inv.ID), map[string]any{"email": norm, "role": role}); err != nil {
		return "", nil, err
	}
	return s.cfg.BaseURL + "/invite/" + token, inv, nil
}

// Invitations lists the org's open invitations, for those who manage members.
func (s *Service) Invitations(ctx context.Context, a Actor) ([]*model.Invitation, error) {
	if err := a.requireToRead(PermMembersWrite); err != nil {
		return nil, err
	}
	return s.store.OpenInvitations(ctx, a.OrgID)
}

// RevokeInvitation withdraws an open invitation; its link stops working.
func (s *Service) RevokeInvitation(ctx context.Context, a Actor, invitationID uuid.UUID) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteInvitation(ctx, a.OrgID, invitationID); err != nil {
			return notFound(err, "invitation")
		}
		return s.audit(ctx, tx, a, "member.invite_revoke", id.Format(id.Invitation, invitationID), nil)
	})
}

// Invitation finds the open invitation a link's token names.
func (s *Service) Invitation(ctx context.Context, token string) (*model.Invitation, error) {
	if token == "" {
		return nil, errInvitationInvalid
	}
	inv, err := s.store.InvitationByToken(ctx, authn.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, errInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if inv.AcceptedAt != nil || !s.Now().Before(inv.ExpiresAt) {
		return nil, errInvitationInvalid
	}
	return inv, nil
}

// AcceptInvitation adds u to the invitation's org with its role. It must be
// the invited person: the same email, so a forwarded link cannot be used
// by someone else's account.
func (s *Service) AcceptInvitation(ctx context.Context, token string, u *model.User, requestID string) (*model.Invitation, error) {
	inv, err := s.Invitation(ctx, token)
	if err != nil {
		return nil, err
	}
	invited, _ := NormalizeEmail(inv.Email)
	if mine, _ := NormalizeEmail(u.Email); mine != invited {
		return nil, apperr.Forbidden("This invitation is for %s. Sign in as them to accept it.", inv.Email)
	}
	a := Actor{OrgID: inv.OrgID, UserID: &u.ID, Role: inv.Role, RequestID: requestID}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.AcceptInvitation(ctx, inv.ID, s.Now()); err != nil {
			return notFoundAs(err, errInvitationInvalid)
		}
		err := tx.AddMember(ctx, &model.Membership{OrgID: inv.OrgID, UserID: u.ID, Role: inv.Role})
		switch {
		case errors.Is(err, store.ErrConflict):
			return nil // a member already: the invitation is spent, nothing changes
		case err != nil:
			return err
		}
		if err := s.notifyJoined(ctx, tx, inv.OrgID, u.ID, u.Email, inv.Role, &u.ID); err != nil {
			return err
		}
		detail := map[string]any{"role": inv.Role, "invitation": id.Format(id.Invitation, inv.ID)}
		if inv.InvitedBy != nil {
			detail["invited_by"] = id.Format(id.User, *inv.InvitedBy)
		}
		return s.audit(ctx, tx, a, "member.add", id.Format(id.User, u.ID), detail)
	})
	return inv, err
}

// AcceptInvitationNewAccount creates the invited person's account, with
// the password they choose, and accepts the invitation with it. Someone
// who has an account already signs in to accept instead.
func (s *Service) AcceptInvitationNewAccount(ctx context.Context, token, name, password, requestID string) (*model.User, *model.Invitation, error) {
	inv, err := s.Invitation(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.CreateUser(ctx, inv.Email, name, password)
	if err != nil {
		if apperr.As(err).Code == "email_taken" {
			return nil, nil, apperr.Conflict("account_exists", "%s has an account already: sign in to accept the invitation.", inv.Email)
		}
		return nil, nil, err
	}
	inv, err = s.AcceptInvitation(ctx, token, u, requestID)
	return u, inv, err
}

// notFoundAs turns store.ErrNotFound into e.
func notFoundAs(err error, e error) error {
	if errors.Is(err, store.ErrNotFound) {
		return e
	}
	return err
}
