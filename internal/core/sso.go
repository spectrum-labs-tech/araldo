// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Single sign-on (ADR 0033): an org connects its OpenID Connect identity
// provider and proves, with a DNS TXT record, that it holds the email
// domains its people sign in with. Anyone the provider vouches for at one
// of those domains signs in, and joins the org with its default role the
// first time.

const (
	// SSOStateTTL is how long a sign-in may spend at the provider.
	SSOStateTTL = 10 * time.Minute
	// SSOVerifyPrefix names the TXT record that proves a domain:
	// _araldo-verify.<domain>, holding "araldo-verify=<token>".
	SSOVerifyPrefix = "_araldo-verify."
	ssoVerifyValue  = "araldo-verify="
	maxSSODomains   = 20
	ssoHTTPTimeout  = 10 * time.Second
)

var (
	errSSOFailed = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "sso_failed",
		Message: "Single sign-on did not complete. Start again, and ask your administrator if it keeps failing."}
	errSSOExpired = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "sso_expired", Message: "That sign-in took too long or was used already. Start again."}
)

// SSOCallbackURL is where providers send people back: the redirect URI an
// org registers with its provider.
func (s *Service) SSOCallbackURL() string { return s.cfg.BaseURL + "/login/sso/callback" }

func ssoSecretAAD(orgID uuid.UUID) string {
	return keyring.AAD("sso_connections", "client_secret", orgID)
}

// SSOSettings is an org's single sign-on, for its settings page.
type SSOSettings struct {
	Connection *model.SSOConnection // nil until connected
	Domains    []*model.SSODomain
	Required   bool
}

// SSO returns the org's single sign-on settings. Owners only.
func (s *Service) SSO(ctx context.Context, a Actor) (*SSOSettings, error) {
	if err := a.require(PermOrgWrite); err != nil {
		return nil, err
	}
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return nil, notFound(err, "org")
	}
	out := &SSOSettings{Required: o.RequireSSO}
	c, _, err := s.store.SSOConnection(ctx, a.OrgID)
	switch {
	case err == nil:
		out.Connection = c
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	if out.Domains, err = s.store.SSODomains(ctx, a.OrgID); err != nil {
		return nil, err
	}
	return out, nil
}

// SSOConnectionInput connects an identity provider. An empty ClientSecret
// keeps the one already stored.
type SSOConnectionInput struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	DefaultRole  model.Role
}

// SaveSSOConnection connects or changes the org's identity provider, after
// fetching its discovery document to check the issuer. Owners only, in
// sudo mode: whoever controls the provider can sign in as anyone at the
// org's domains.
func (s *Service) SaveSSOConnection(ctx context.Context, a Actor, ss *model.Session, in SSOConnectionInput) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	if err := s.requireSudoFor(a, ss); err != nil {
		return err
	}
	var ps apperr.Problems
	issuer := strings.TrimSpace(in.Issuer)
	if u, err := url.Parse(issuer); err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		ps.Add("issuer_invalid", "issuer", "The issuer is the provider's https:// URL, as its documentation gives it.")
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" || len(clientID) > 500 {
		ps.Add("client_id_invalid", "client_id", "Enter the client ID the provider gave you.")
	}
	secret := strings.TrimSpace(in.ClientSecret)
	if len(secret) > 2000 {
		ps.Add("client_secret_invalid", "client_secret", "That client secret is too long.")
	}
	if !in.DefaultRole.Valid() || in.DefaultRole == model.RoleOwner {
		ps.Add("default_role_invalid", "default_role", "The default role is admin, editor or viewer.")
	}
	_, _, existing := s.store.SSOConnection(ctx, a.OrgID)
	if secret == "" && errors.Is(existing, store.ErrNotFound) {
		ps.Add("client_secret_required", "client_secret", "Enter the client secret the provider gave you.")
	}
	if err := ps.Err("The connection is not valid."); err != nil {
		return err
	}
	if _, err := oidc.NewProvider(oidc.ClientContext(ctx, s.SSOHTTP), issuer); err != nil {
		s.log.InfoContext(ctx, "sso discovery failed", "org", a.OrgID, "err", err)
		return apperr.Invalid("issuer_unreachable", "issuer",
			"Could not read the provider's configuration from %s/.well-known/openid-configuration. Check the issuer URL.", strings.TrimSuffix(issuer, "/"))
	}
	c := &model.SSOConnection{OrgID: a.OrgID, Issuer: issuer, ClientID: clientID, DefaultRole: in.DefaultRole}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		var sealed []byte
		if secret != "" {
			var err error
			if sealed, err = s.keys.With(tx).Encrypt(ctx, a.OrgID, ssoSecretAAD(a.OrgID), []byte(secret)); err != nil { // through tx: see keyring.With
				return err
			}
		}
		if err := tx.SaveSSOConnection(ctx, c, sealed); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "sso.connect", id.Format(id.Org, a.OrgID),
			map[string]any{"issuer": issuer, "client_id": clientID, "default_role": in.DefaultRole, "secret_changed": secret != ""})
	})
}

// DeleteSSOConnection disconnects the org's identity provider, unless the
// org requires single sign-on. Owners only, in sudo mode.
func (s *Service) DeleteSSOConnection(ctx context.Context, a Actor, ss *model.Session) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	if err := s.requireSudoFor(a, ss); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		o, err := tx.Org(ctx, a.OrgID)
		if err != nil {
			return notFound(err, "org")
		}
		if o.RequireSSO {
			return apperr.Conflict("sso_required", "Stop requiring single sign-on before disconnecting the provider.")
		}
		if err := tx.DeleteSSOConnection(ctx, a.OrgID); err != nil {
			return notFound(err, "SSO connection")
		}
		return s.audit(ctx, tx, a, "sso.disconnect", id.Format(id.Org, a.OrgID), nil)
	})
}

// NormalizeDomain lowercases a domain name and checks its form: letters,
// digits and hyphens (punycode for others), in two labels or more.
func NormalizeDomain(domain string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	bad := apperr.Invalid("domain_invalid", "domain", "%q is not a domain name such as araldo.dev.", domain)
	labels := strings.Split(d, ".")
	if len(d) > 253 || len(labels) < 2 {
		return "", bad
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", bad
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", bad
			}
		}
	}
	return d, nil
}

// AddSSODomain adds a domain for the org to verify, and returns it with the
// token its TXT record must hold. Owners only.
func (s *Service) AddSSODomain(ctx context.Context, a Actor, domain string) (*model.SSODomain, error) {
	if err := a.require(PermOrgWrite); err != nil {
		return nil, err
	}
	d, err := NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	have, err := s.store.SSODomains(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	if len(have) >= maxSSODomains {
		return nil, apperr.Invalid("domains_limit", "domain", "An org has at most %d domains.", maxSSODomains)
	}
	sd := &model.SSODomain{OrgID: a.OrgID, Domain: d, Token: randomToken(), CreatedAt: s.Now()}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.AddSSODomain(ctx, sd); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("domain_exists", "%s is already added.", d)
			}
			return err
		}
		return s.audit(ctx, tx, a, "sso.domain_add", id.Format(id.Org, a.OrgID), map[string]any{"domain": d})
	})
	return sd, err
}

// VerifySSODomain looks for the domain's TXT record and, once it is there,
// marks the domain verified: people with email there then sign in through
// the org's provider. A domain is verified for one org at most.
func (s *Service) VerifySSODomain(ctx context.Context, a Actor, domain string) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	d, err := NormalizeDomain(domain)
	if err != nil {
		return err
	}
	var sd *model.SSODomain
	all, err := s.store.SSODomains(ctx, a.OrgID)
	if err != nil {
		return err
	}
	for _, x := range all {
		if x.Domain == d {
			sd = x
		}
	}
	if sd == nil {
		return apperr.NotFound("domain")
	}
	if sd.VerifiedAt != nil {
		return nil
	}
	records, err := s.LookupTXT(ctx, SSOVerifyPrefix+d)
	found := false
	for _, r := range records {
		if strings.TrimSpace(r) == ssoVerifyValue+sd.Token {
			found = true
		}
	}
	if !found {
		if err != nil {
			s.log.InfoContext(ctx, "sso domain lookup failed", "domain", d, "err", err)
		}
		return apperr.Invalid("domain_unverified", "domain",
			"No TXT record at %s%s holds %s%s yet. DNS changes can take a while to appear; try again later.", SSOVerifyPrefix, d, ssoVerifyValue, sd.Token)
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.VerifySSODomain(ctx, a.OrgID, d, s.Now()); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("domain_taken", "%s is verified by another org.", d)
			}
			return notFound(err, "domain")
		}
		return s.audit(ctx, tx, a, "sso.domain_verify", id.Format(id.Org, a.OrgID), map[string]any{"domain": d})
	})
}

// RemoveSSODomain removes one of the org's domains; not the last verified
// one while the org requires single sign-on.
func (s *Service) RemoveSSODomain(ctx context.Context, a Actor, domain string) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return s.store.InTx(ctx, func(tx *store.Store) error {
		o, err := tx.Org(ctx, a.OrgID)
		if err != nil {
			return notFound(err, "org")
		}
		if o.RequireSSO {
			all, err := tx.SSODomains(ctx, a.OrgID)
			if err != nil {
				return err
			}
			if verifiedExcept(all, d) == 0 {
				return apperr.Conflict("sso_required", "Stop requiring single sign-on before removing its last verified domain.")
			}
		}
		if err := tx.DeleteSSODomain(ctx, a.OrgID, d); err != nil {
			return notFound(err, "domain")
		}
		return s.audit(ctx, tx, a, "sso.domain_remove", id.Format(id.Org, a.OrgID), map[string]any{"domain": d})
	})
}

func verifiedExcept(all []*model.SSODomain, except string) int {
	n := 0
	for _, d := range all {
		if d.VerifiedAt != nil && d.Domain != except {
			n++
		}
	}
	return n
}

// SetRequireSSO sets whether members reach the org only signed in through
// its single sign-on: the dashboard, and CLI tokens approved from such a
// session. Owners turn it on from a session that came through it, so they
// cannot lock themselves out, and off in sudo mode. The operator can do
// either (araldo admin org update), to recover an org whose provider is
// gone.
func (s *Service) SetRequireSSO(ctx context.Context, a Actor, ss *model.Session, on bool) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		o, err := tx.Org(ctx, a.OrgID)
		if err != nil {
			return notFound(err, "org")
		}
		if o.RequireSSO == on {
			return nil
		}
		if on {
			if _, _, err := tx.SSOConnection(ctx, a.OrgID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return apperr.Invalid("sso_not_connected", "require_sso", "Connect an identity provider first.")
				}
				return err
			}
			all, err := tx.SSODomains(ctx, a.OrgID)
			if err != nil {
				return err
			}
			if verifiedExcept(all, "") == 0 {
				return apperr.Invalid("sso_no_domain", "require_sso", "Verify a domain first.")
			}
			if !a.Operator && (ss == nil || ss.SSOOrg == nil || *ss.SSOOrg != a.OrgID) {
				return apperr.Invalid("sso_session_required", "require_sso",
					"Sign in through single sign-on first, so you know it works before everyone must use it.")
			}
		} else if err := s.requireSudoFor(a, ss); err != nil {
			return err
		}
		if err := tx.SetOrgRequireSSO(ctx, a.OrgID, on); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "org.update", id.Format(id.Org, a.OrgID), map[string]any{"require_sso": on})
	})
}

// ssoClient is an org's provider and its OAuth client.
func (s *Service) ssoClient(ctx context.Context, orgID uuid.UUID) (*oidc.Provider, *oauth2.Config, *model.SSOConnection, error) {
	c, sealed, err := s.store.SSOConnection(ctx, orgID)
	if err != nil {
		return nil, nil, nil, err
	}
	secret, err := s.keys.Decrypt(ctx, orgID, ssoSecretAAD(orgID), sealed)
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, s.SSOHTTP), c.Issuer)
	if err != nil {
		s.log.WarnContext(ctx, "sso discovery failed", "org", orgID, "err", err)
		return nil, nil, nil, &apperr.Error{Kind: apperr.KindUnavailable, Code: "sso_provider_unavailable",
			Message: "Your organization's identity provider could not be reached. Try again in a moment."}
	}
	cfg := &oauth2.Config{ClientID: c.ClientID, ClientSecret: string(secret), Endpoint: p.Endpoint(), RedirectURL: s.SSOCallbackURL(),
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
	return p, cfg, c, nil
}

// emailDomain is the domain of a normalized address.
func emailDomain(email string) string { return email[strings.LastIndexByte(email, '@')+1:] }

// BeginSSO starts signing in someone by their email: it finds the org that
// verified its domain and returns the provider's sign-in URL, and the state
// to keep in the browser until it comes back (FinishSSO).
func (s *Service) BeginSSO(ctx context.Context, email, next string) (redirect, state string, err error) {
	norm, err := NormalizeEmail(email)
	if err != nil {
		return "", "", err
	}
	orgID, err := s.store.SSODomainOrg(ctx, emailDomain(norm))
	if errors.Is(err, store.ErrNotFound) {
		return "", "", apperr.Invalid("sso_unavailable", "email", "Single sign-on is not set up for %s. Sign in with your password instead.", emailDomain(norm))
	}
	if err != nil {
		return "", "", err
	}
	_, cfg, _, err := s.ssoClient(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", apperr.Invalid("sso_unavailable", "email", "Single sign-on is not set up for %s. Sign in with your password instead.", emailDomain(norm))
	}
	if err != nil {
		return "", "", err
	}
	state, hash := authn.NewToken()
	st := &store.SSOState{OrgID: orgID, Verifier: oauth2.GenerateVerifier(), Nonce: randomToken(), Next: next, ExpiresAt: s.Now().Add(SSOStateTTL)}
	if err := s.store.CreateSSOState(ctx, hash, st); err != nil {
		return "", "", err
	}
	return cfg.AuthCodeURL(state, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier), oauth2.SetAuthURLParam("login_hint", norm)), state, nil
}

// ssoClaims are the ID token claims sign-in reads.
type ssoClaims struct {
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

// SSOResult is a finished single sign-on: the session, and where the
// person was going.
type SSOResult struct {
	*LoginResult
	Next string
}

// FinishSSO completes a sign-in when the provider sends the person back
// with code: it redeems the code (with the PKCE verifier), checks the ID
// token (issuer, audience, signature, expiry, nonce) and that its email is
// at one of the org's verified domains, and signs that person in. Someone
// new gets an account without a password; someone not yet a member joins
// with the org's default role. The session needs no second factor (the
// provider is the org's) and is in the org.
func (s *Service) FinishSSO(ctx context.Context, state, code, userAgent, ip string) (*SSOResult, error) {
	st, err := s.store.TakeSSOState(ctx, authn.HashToken(state), s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errSSOExpired
	}
	if err != nil {
		return nil, err
	}
	p, cfg, conn, err := s.ssoClient(ctx, st.OrgID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errSSOFailed
	}
	if err != nil {
		return nil, err
	}
	hctx := oidc.ClientContext(ctx, s.SSOHTTP)
	tok, err := cfg.Exchange(hctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.log.InfoContext(ctx, "sso code exchange failed", "org", st.OrgID, "err", err)
		return nil, errSSOFailed
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: conn.ClientID, Now: s.Now}).Verify(hctx, raw)
	if err != nil || idt.Nonce != st.Nonce {
		s.log.InfoContext(ctx, "sso id token refused", "org", st.OrgID, "err", err)
		return nil, errSSOFailed
	}
	var claims ssoClaims
	if err := idt.Claims(&claims); err != nil {
		return nil, errSSOFailed
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return nil, &apperr.Error{Kind: apperr.KindForbidden, Code: "sso_email_unverified", Message: "Your identity provider has not verified your email address."}
	}
	email, err := NormalizeEmail(claims.Email)
	if err != nil {
		return nil, &apperr.Error{Kind: apperr.KindForbidden, Code: "sso_email_missing", Message: "Your identity provider did not share a usable email address."}
	}
	if owner, err := s.store.SSODomainOrg(ctx, emailDomain(email)); err != nil || owner != st.OrgID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, &apperr.Error{Kind: apperr.KindForbidden, Code: "sso_domain_mismatch",
			Message: emailDomain(email) + " is not a domain this organization signs in with."}
	}
	u, err := s.ssoUser(ctx, conn, email, claims.Name)
	if err != nil {
		return nil, err
	}
	res, err := s.startSession(ctx, u, userAgent, ip, s.Now(), true, &st.OrgID)
	if err != nil {
		return nil, err
	}
	return &SSOResult{LoginResult: res, Next: st.Next}, nil
}

// ssoUser finds the person by email, or makes their account, and makes
// them a member of conn's org if they are not one.
func (s *Service) ssoUser(ctx context.Context, conn *model.SSOConnection, email, name string) (*model.User, error) {
	u, err := s.store.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		u = &model.User{ID: id.New(), Email: email, Name: truncate(strings.TrimSpace(name), 100)}
		if err = s.store.CreateUser(ctx, u, email); errors.Is(err, store.ErrConflict) {
			u, err = s.store.UserByEmail(ctx, email) // made a moment ago, by another sign-in
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.store.Member(ctx, conn.OrgID, u.ID); err == nil {
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	a := Actor{OrgID: conn.OrgID, UserID: &u.ID, Role: conn.DefaultRole, RequestID: "sso"}
	if err := s.checkLimit(ctx, a, limitMembers, 1, email); err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		err := tx.AddMember(ctx, &model.Membership{OrgID: conn.OrgID, UserID: u.ID, Role: conn.DefaultRole})
		switch {
		case errors.Is(err, store.ErrConflict):
			return nil // joined a moment ago, by another sign-in
		case err != nil:
			return err
		}
		return s.audit(ctx, tx, a, "member.add", id.Format(id.User, u.ID), map[string]any{"role": conn.DefaultRole, "via": "sso"})
	})
	return u, err
}
