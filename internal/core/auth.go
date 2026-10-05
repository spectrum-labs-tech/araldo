// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Session lifetimes (ADR 0007).
const (
	SessionIdle       = 7 * 24 * time.Hour
	SessionAbsolute   = 30 * 24 * time.Hour
	PendingMFAWindow  = 10 * time.Minute
	SudoWindow        = 10 * time.Minute
	lockAfterFailures = 10
	recoveryCodeCount = 10
)

// ErrBadCredentials is the one answer for every failed sign-in, so it
// never reveals whether an email exists.
var ErrBadCredentials = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "bad_credentials", Message: "Email or password is incorrect."}

// NormalizeEmail lowercases and trims an address, and checks its form.
func NormalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || !strings.Contains(e, ".") {
		return "", apperr.Invalid("email_invalid", "email", "%q is not a valid email address.", email)
	}
	return e, nil
}

// CreateUser adds a user with a password.
func (s *Service) CreateUser(ctx context.Context, email, name, password string) (*model.User, error) {
	norm, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := authn.ValidatePassword(password); err != nil {
		return nil, apperr.Invalid("password_invalid", "password", "%s", upperFirst(err.Error()))
	}
	hash, err := authn.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &model.User{ID: id.New(), Email: strings.TrimSpace(email), Name: strings.TrimSpace(name), PasswordHash: hash}
	if err := s.store.CreateUser(ctx, u, norm); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, apperr.Conflict("email_taken", "A user with that email already exists.")
		}
		return nil, err
	}
	return u, nil
}

// User returns a user by ID.
func (s *Service) User(ctx context.Context, userID uuid.UUID) (*model.User, error) {
	u, err := s.store.User(ctx, userID)
	return u, notFound(err, "user")
}

// UserByEmail returns a user by email.
func (s *Service) UserByEmail(ctx context.Context, email string) (*model.User, error) {
	norm, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	u, err := s.store.UserByEmail(ctx, norm)
	return u, notFound(err, "user")
}

// LoginResult is a started session.
type LoginResult struct {
	Token    string
	Session  *model.Session
	User     *model.User
	NeedsMFA bool
}

// Login checks a password and starts a session, which is pending until the
// second factor is given when the user has one.
func (s *Service) Login(ctx context.Context, email, password, userAgent, ip string) (*LoginResult, error) {
	now := s.Now()
	norm, err := NormalizeEmail(email)
	if err != nil {
		authn.SpendPasswordTime(password)
		return nil, ErrBadCredentials
	}
	u, err := s.store.UserByEmail(ctx, norm)
	if errors.Is(err, store.ErrNotFound) {
		authn.SpendPasswordTime(password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := locked(u, now); err != nil {
		authn.SpendPasswordTime(password)
		return nil, err
	}
	ok, rehash := false, false
	if u.PasswordHash != "" {
		ok, rehash, err = authn.VerifyPassword(password, u.PasswordHash)
		if err != nil {
			return nil, err
		}
	} else {
		authn.SpendPasswordTime(password)
	}
	if !ok {
		if err := s.recordFailure(ctx, u, now); err != nil {
			return nil, err
		}
		return nil, ErrBadCredentials
	}
	// With a second factor, the count is reset only once that is given too,
	// so retrying the password cannot buy fresh guesses at the code.
	if !u.MFAEnabled() {
		if err := s.resetFailures(ctx, u); err != nil {
			return nil, err
		}
	}
	if rehash {
		if h, err := authn.HashPassword(password); err == nil {
			_ = s.store.SetPassword(ctx, u.ID, h)
		}
	}
	return s.startSession(ctx, u, userAgent, ip, now)
}

// locked refuses a user whose account is locked after failed attempts.
func locked(u *model.User, now time.Time) error {
	if u.LockedUntil == nil || !now.Before(*u.LockedUntil) {
		return nil
	}
	return &apperr.Error{Kind: apperr.KindRateLimited, Code: "account_locked",
		Message: "Too many failed sign-ins. Try again after " + u.LockedUntil.UTC().Format("15:04 MST") + "."}
}

// recordFailure counts a wrong password or code against the account (ADR
// 0007), locking it for longer after each failure past lockAfterFailures.
func (s *Service) recordFailure(ctx context.Context, u *model.User, now time.Time) error {
	var lock *time.Time
	if u.FailedLogins+1 >= lockAfterFailures {
		d := min(time.Minute<<min(u.FailedLogins+1-lockAfterFailures, 6), time.Hour)
		lock = ptr(now.Add(d))
	}
	return s.store.RecordLoginFailure(ctx, u.ID, lock)
}

func (s *Service) resetFailures(ctx context.Context, u *model.User) error {
	if u.FailedLogins == 0 && u.LockedUntil == nil {
		return nil
	}
	return s.store.ResetLoginFailures(ctx, u.ID)
}

func (s *Service) startSession(ctx context.Context, u *model.User, userAgent, ip string, now time.Time) (*LoginResult, error) {
	token, hash := authn.NewToken()
	csrf, _ := authn.NewToken()
	ss := &model.Session{
		ID: id.New(), UserID: u.ID, CSRFToken: csrf, State: model.SessionActive,
		UserAgent: truncate(userAgent, 300), IP: truncate(ip, 64), ExpiresAt: now.Add(SessionIdle), SudoUntil: ptr(now.Add(SudoWindow)),
	}
	if u.MFAEnabled() {
		ss.State, ss.ExpiresAt, ss.SudoUntil = model.SessionPendingMFA, now.Add(PendingMFAWindow), nil
	}
	// Start in the user's first org, in test mode.
	if ms, err := s.store.UserMemberships(ctx, u.ID); err == nil && len(ms) > 0 {
		ss.CurrentOrg = &ms[0].OrgID
	}
	if err := s.store.CreateSession(ctx, ss, hash); err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, Session: ss, User: u, NeedsMFA: ss.State == model.SessionPendingMFA}, nil
}

// Session resolves a session token. Pending sessions are returned too; the
// caller decides what they may do.
func (s *Service) Session(ctx context.Context, token string) (*model.Session, *model.User, error) {
	if token == "" {
		return nil, nil, apperr.ErrUnauthorized
	}
	now := s.Now()
	ss, err := s.store.SessionByToken(ctx, authn.HashToken(token), now)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, apperr.ErrUnauthorized
	}
	if err != nil {
		return nil, nil, err
	}
	u, err := s.store.User(ctx, ss.UserID)
	if err != nil {
		return nil, nil, err
	}
	if ss.State == model.SessionActive && now.Sub(ss.LastSeenAt) > time.Minute {
		_ = s.store.TouchSession(ctx, ss.ID, now, now.Add(SessionIdle), ss.CreatedAt.Add(SessionAbsolute))
	}
	return ss, u, nil
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, sessionID uuid.UUID) error {
	return s.store.DeleteSession(ctx, sessionID)
}

// VerifySecondFactor completes a pending session with a TOTP code or a
// recovery code.
func (s *Service) VerifySecondFactor(ctx context.Context, ss *model.Session, code string) error {
	if ss.State != model.SessionPendingMFA {
		return nil
	}
	if err := s.checkSecondFactor(ctx, ss.UserID, code); err != nil {
		return err
	}
	return s.store.ActivateSession(ctx, ss.ID, s.Now().Add(SudoWindow))
}

// checkSecondFactor checks a TOTP or recovery code. A wrong one counts
// against the account like a wrong password, and a locked account is
// refused, so codes cannot be guessed (ADR 0007); a right one resets the
// count.
func (s *Service) checkSecondFactor(ctx context.Context, userID uuid.UUID, code string) error {
	bad := &apperr.Error{Kind: apperr.KindUnauthorized, Code: "bad_code", Message: "That code is not valid."}
	var u *model.User
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if u, err = tx.UserForUpdate(ctx, userID); err != nil {
			return err
		}
		if !u.MFAEnabled() {
			return nil
		}
		if err := locked(u, s.Now()); err != nil {
			return err
		}
		clean := strings.TrimSpace(code)
		if len(authn.NormalizeRecoveryCode(clean)) == 16 {
			err := tx.UseRecoveryCode(ctx, u.ID, authn.HashToken(authn.NormalizeRecoveryCode(clean)))
			if errors.Is(err, store.ErrNotFound) {
				return bad
			}
			return err
		}
		secret, err := s.keys.Decrypt(ctx, keyring.Install, totpAAD(u.ID), u.TOTPSecret)
		if err != nil {
			return err
		}
		step, ok := authn.VerifyTOTP(secret, clean, s.Now(), u.TOTPLastStep)
		if !ok {
			return bad
		}
		if err := tx.AdvanceTOTPStep(ctx, u.ID, step); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return bad // used a moment ago
			}
			return err
		}
		return nil
	})
	switch {
	case err == bad: //nolint:errorlint // bad is this call's own value
		if ferr := s.recordFailure(ctx, u, s.Now()); ferr != nil {
			return ferr
		}
		return bad
	case err != nil:
		return err
	}
	return s.resetFailures(ctx, u)
}

func totpAAD(userID uuid.UUID) string { return keyring.AAD("users", "totp_secret", userID) }

// Reauthenticate grants sudo mode (ADR 0007) after checking the password,
// and the second factor when the user has one.
func (s *Service) Reauthenticate(ctx context.Context, ss *model.Session, password, code string) error {
	u, err := s.store.User(ctx, ss.UserID)
	if err != nil {
		return err
	}
	now := s.Now()
	if err := locked(u, now); err != nil {
		authn.SpendPasswordTime(password)
		return err
	}
	ok, _, err := authn.VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		// Counted like a sign-in, so a stolen session cannot guess the
		// password here to reach sudo mode.
		if err := s.recordFailure(ctx, u, now); err != nil {
			return err
		}
		return &apperr.Error{Kind: apperr.KindUnauthorized, Code: "bad_password", Message: "Password is incorrect."}
	}
	if u.MFAEnabled() {
		if err := s.checkSecondFactor(ctx, u.ID, code); err != nil {
			return err
		}
	} else if err := s.resetFailures(ctx, u); err != nil {
		return err
	}
	return s.store.SetSessionSudo(ctx, ss.ID, s.Now().Add(SudoWindow))
}

// InSudo reports whether the session re-authenticated recently.
func (s *Service) InSudo(ss *model.Session) bool {
	return ss.SudoUntil != nil && s.Now().Before(*ss.SudoUntil)
}

func (s *Service) requireSudo(ss *model.Session) error {
	if !s.InSudo(ss) {
		return &apperr.Error{Kind: apperr.KindForbidden, Code: "reauthentication_required", Message: "Confirm your password to continue."}
	}
	return nil
}

// BeginTOTP makes a new, not yet enabled TOTP secret for the user and
// returns it for the authenticator app.
func (s *Service) BeginTOTP(ctx context.Context, ss *model.Session) (secretText, uri string, err error) {
	if err := s.requireSudo(ss); err != nil {
		return "", "", err
	}
	u, err := s.store.User(ctx, ss.UserID)
	if err != nil {
		return "", "", err
	}
	if u.MFAEnabled() {
		return "", "", apperr.Conflict("mfa_enabled", "Two-factor authentication is already on.")
	}
	secret, err := authn.NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	sealed, err := s.keys.Encrypt(ctx, keyring.Install, totpAAD(u.ID), secret)
	if err != nil {
		return "", "", err
	}
	if err := s.store.SetTOTP(ctx, u.ID, sealed, nil); err != nil {
		return "", "", err
	}
	return authn.TOTPSecretText(secret), authn.TOTPURI("Araldo", u.Email, secret), nil
}

// ConfirmTOTP turns TOTP on once the user proves the app works, and returns
// fresh recovery codes (shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, ss *model.Session, code string) ([]string, error) {
	if err := s.requireSudo(ss); err != nil {
		return nil, err
	}
	var codes []string
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		u, err := tx.UserForUpdate(ctx, ss.UserID)
		if err != nil {
			return err
		}
		if u.MFAEnabled() || len(u.TOTPSecret) == 0 {
			return apperr.Conflict("mfa_not_pending", "Start two-factor setup again.")
		}
		secret, err := s.keys.Decrypt(ctx, keyring.Install, totpAAD(u.ID), u.TOTPSecret)
		if err != nil {
			return err
		}
		step, ok := authn.VerifyTOTP(secret, code, s.Now(), 0)
		if !ok {
			return &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_code", Param: "code", Message: "That code is not valid. Check the time on your device."}
		}
		if err := tx.SetTOTP(ctx, u.ID, u.TOTPSecret, ptr(s.Now())); err != nil {
			return err
		}
		if err := tx.AdvanceTOTPStep(ctx, u.ID, step); err != nil {
			return err
		}
		codes, err = replaceRecoveryCodes(ctx, tx, u.ID)
		return err
	})
	return codes, err
}

// RegenerateRecoveryCodes replaces the user's recovery codes.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, ss *model.Session) ([]string, error) {
	if err := s.requireSudo(ss); err != nil {
		return nil, err
	}
	var codes []string
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		codes, err = replaceRecoveryCodes(ctx, tx, ss.UserID)
		return err
	})
	return codes, err
}

func replaceRecoveryCodes(ctx context.Context, tx *store.Store, userID uuid.UUID) ([]string, error) {
	codes := authn.NewRecoveryCodes(recoveryCodeCount)
	hashes := make([][]byte, len(codes))
	for i, c := range codes {
		hashes[i] = authn.HashToken(authn.NormalizeRecoveryCode(c))
	}
	return codes, tx.ReplaceRecoveryCodes(ctx, userID, hashes)
}

// DisableTOTP turns two-factor authentication off, unless an org the user
// belongs to requires it.
func (s *Service) DisableTOTP(ctx context.Context, ss *model.Session) error {
	if err := s.requireSudo(ss); err != nil {
		return err
	}
	ms, err := s.store.UserMemberships(ctx, ss.UserID)
	if err != nil {
		return err
	}
	for _, m := range ms {
		o, err := s.store.Org(ctx, m.OrgID)
		if err != nil {
			return err
		}
		if o.RequireMFA {
			return apperr.Forbidden("%s requires two-factor authentication.", o.Name)
		}
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetTOTP(ctx, ss.UserID, nil, nil); err != nil {
			return err
		}
		return tx.ReplaceRecoveryCodes(ctx, ss.UserID, nil)
	})
}

// RemainingRecoveryCodes counts unused recovery codes.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.store.RemainingRecoveryCodes(ctx, userID)
}

// ChangePassword sets a new password and signs out other sessions.
func (s *Service) ChangePassword(ctx context.Context, ss *model.Session, current, next string) error {
	u, err := s.store.User(ctx, ss.UserID)
	if err != nil {
		return err
	}
	ok, _, err := authn.VerifyPassword(current, u.PasswordHash)
	if err != nil || !ok {
		return apperr.Invalid("bad_password", "current_password", "Current password is incorrect.")
	}
	if err := authn.ValidatePassword(next); err != nil {
		return apperr.Invalid("password_invalid", "new_password", "%s", upperFirst(err.Error()))
	}
	hash, err := authn.HashPassword(next)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		return tx.DeleteOtherSessions(ctx, u.ID, ss.ID)
	})
}

// ResetPassword sets a user's password (operator CLI; ADR 0007) and signs
// them out everywhere.
func (s *Service) ResetPassword(ctx context.Context, email, password string) error {
	u, err := s.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if err := authn.ValidatePassword(password); err != nil {
		return apperr.Invalid("password_invalid", "password", "%s", upperFirst(err.Error()))
	}
	hash, err := authn.HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		if err := tx.ResetLoginFailures(ctx, u.ID); err != nil {
			return err
		}
		return tx.DeleteOtherSessions(ctx, u.ID, uuid.Nil)
	})
}

// SwitchContext changes the session's org and mode.
func (s *Service) SwitchContext(ctx context.Context, ss *model.Session, orgID uuid.UUID, livemode bool) error {
	if _, err := s.store.Membership(ctx, orgID, ss.UserID); err != nil {
		return notFound(err, "org")
	}
	return s.store.SetSessionContext(ctx, ss.ID, &orgID, livemode)
}

// MemberActor builds the actor for a signed-in member of org, in mode.
func (s *Service) MemberActor(ctx context.Context, userID, orgID uuid.UUID, livemode bool, requestID string) (Actor, *model.Membership, error) {
	m, err := s.store.Membership(ctx, orgID, userID)
	if err != nil {
		return Actor{}, nil, notFound(err, "org")
	}
	return Actor{OrgID: orgID, Livemode: livemode, UserID: &userID, Role: m.Role, RequestID: requestID}, m, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
