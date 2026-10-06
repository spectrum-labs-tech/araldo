// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Password resets by email (ADR 0034).

const (
	// PasswordResetTTL is how long a reset link works.
	PasswordResetTTL = 30 * time.Minute
	// passwordResetsPerHour is how many links an account gets an hour.
	passwordResetsPerHour = 3
)

var errResetInvalid = &apperr.Error{Kind: apperr.KindNotFound, Code: "reset_link_invalid",
	Message: "This reset link has expired or was used already. Ask for a new one."}

// RequestPasswordReset emails a password reset link to the account with
// email, if there is one. It returns nil whether or not there is, so the
// answer reveals no accounts; callers run it apart from the request (see
// the dashboard), so its time reveals none either. An address at a domain
// an org signs in with by single sign-on gets a link to sign in that way
// instead. An account gets at most three links an hour.
func (s *Service) RequestPasswordReset(ctx context.Context, email, ip string) error {
	if s.cfg.Mail == nil {
		return errMailOff
	}
	norm, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	u, err := s.store.UserByEmail(ctx, norm)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if orgID, err := s.store.SSODomainOrg(ctx, emailDomain(norm)); err == nil {
		o, err := s.store.Org(ctx, orgID)
		if err != nil {
			return err
		}
		return s.sendMail(ctx, mailContent{To: u.Email, Subject: "Sign in to Araldo with single sign-on",
			Paragraphs: []string{"Someone asked to reset the password of your Araldo account.",
				o.Name + " signs you in with single sign-on, so your identity provider manages your password. Sign in through it instead."},
			ButtonLabel: "Sign in with single sign-on", ButtonURL: s.cfg.BaseURL + "/login/sso?email=" + url.QueryEscape(u.Email),
			Footer: "If you did not ask for this, you can ignore this email."})
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	now := s.Now()
	n, err := s.store.CountPasswordResets(ctx, u.ID, now.Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= passwordResetsPerHour {
		s.log.InfoContext(ctx, "password reset not sent: too many this hour", "user", u.ID)
		return nil
	}
	token, hash := authn.NewToken()
	if err := s.store.CreatePasswordReset(ctx, hash, u.ID, truncate(ip, 64), now.Add(PasswordResetTTL)); err != nil {
		return err
	}
	return s.sendMail(ctx, mailContent{To: u.Email, Subject: "Reset your Araldo password",
		Paragraphs: []string{"Someone asked to reset the password of your Araldo account. If it was you, choose a new one with this link within 30 minutes.",
			"Setting it signs you out everywhere, the araldo CLI included."},
		ButtonLabel: "Choose a new password", ButtonURL: s.cfg.BaseURL + "/login/reset/" + token,
		Footer: "If you did not ask for this, ignore this email: your password stays as it is."})
}

// sendMail renders and sends an email at once.
func (s *Service) sendMail(ctx context.Context, c mailContent) error {
	m, err := c.message()
	if err != nil {
		return err
	}
	return s.cfg.Mail.Send(ctx, m)
}

// CheckPasswordReset reports whether a reset link still works.
func (s *Service) CheckPasswordReset(ctx context.Context, token string) error {
	_, err := s.store.PasswordResetUser(ctx, authn.HashToken(token), s.Now())
	return notFoundAs(err, errResetInvalid)
}

// ResetPasswordWithLink sets a new password with a reset link, once, and
// signs the person out everywhere, the CLI included, as the operator's
// reset does: it is how a taken-over account is taken back.
func (s *Service) ResetPasswordWithLink(ctx context.Context, token, password string) error {
	if err := authn.ValidatePassword(password); err != nil {
		return apperr.Invalid("password_invalid", "password", "%s", upperFirst(err.Error()))
	}
	hash, err := authn.HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		userID, err := tx.TakePasswordReset(ctx, authn.HashToken(token), s.Now())
		if err != nil {
			return notFoundAs(err, errResetInvalid)
		}
		if err := tx.SetPassword(ctx, userID, hash); err != nil {
			return err
		}
		if err := tx.ResetLoginFailures(ctx, userID); err != nil {
			return err
		}
		if err := tx.RevokeAllUserTokens(ctx, userID, s.Now()); err != nil {
			return err
		}
		if err := tx.DeleteOtherSessions(ctx, userID, uuid.Nil); err != nil {
			return err
		}
		return s.audit(ctx, tx, Actor{UserID: &userID, RequestID: "password-reset"}, "user.password_reset", id.Format(id.User, userID), nil)
	})
}
