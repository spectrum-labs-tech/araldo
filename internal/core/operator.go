// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// The operator API (ADR 0031): an install that hosts orgs for others lets
// its own service create and manage them from outside, with operator keys,
// and sends people out to it to sign up and to pay.

// Operator keys.

// CreateOperatorKey makes a key for the operator API and returns it, shown
// once. Only the operator makes them, with `araldo admin operator-keys`.
func (s *Service) CreateOperatorKey(ctx context.Context, a Actor, name string) (string, *model.OperatorKey, error) {
	if err := requireOperator(a); err != nil {
		return "", nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", nil, apperr.Invalid("name_invalid", "name", "A key needs a name of 1 to 100 characters, such as what uses it.")
	}
	plain := authn.NewOperatorKey()
	k := &model.OperatorKey{ID: id.New(), Name: name, Hint: authn.KeyHint(plain), CreatedAt: s.Now()}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateOperatorKey(ctx, k, authn.HashToken(plain)); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "operator_key.create", id.Format(id.OperatorKey, k.ID), map[string]any{"name": name})
	})
	return plain, k, err
}

// OperatorKeys lists the operator keys not revoked.
func (s *Service) OperatorKeys(ctx context.Context, a Actor) ([]*model.OperatorKey, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	return s.store.OperatorKeys(ctx)
}

// RevokeOperatorKey stops a key working at once.
func (s *Service) RevokeOperatorKey(ctx context.Context, a Actor, keyID uuid.UUID) error {
	if err := requireOperator(a); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RevokeOperatorKey(ctx, keyID, s.Now()); err != nil {
			return notFound(err, "operator key")
		}
		return s.audit(ctx, tx, a, "operator_key.revoke", id.Format(id.OperatorKey, keyID), nil)
	})
}

var errOperatorKeyInvalid = &apperr.Error{Kind: apperr.KindUnauthorized, Code: "operator_key_invalid",
	Message: "Invalid operator key. Operator keys look like ald_op_…; make one with araldo admin operator-keys create."}

// AuthenticateOperatorKey turns an operator key into the operator, acting
// on the whole install.
func (s *Service) AuthenticateOperatorKey(ctx context.Context, plain, requestID string) (Actor, error) {
	if !authn.IsOperatorKey(plain) {
		return Actor{}, errOperatorKeyInvalid
	}
	k, err := s.store.OperatorKeyByHash(ctx, authn.HashToken(plain))
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, errOperatorKeyInvalid
	}
	if err != nil {
		return Actor{}, err
	}
	if k.RevokedAt != nil {
		return Actor{}, &apperr.Error{Kind: apperr.KindUnauthorized, Code: "operator_key_revoked", Message: "This operator key was revoked."}
	}
	_ = s.store.TouchOperatorKey(ctx, k.ID, s.Now())
	return Actor{Operator: true, Role: model.RoleOwner, OperatorKeyID: &k.ID, RequestID: requestID}, nil
}

// InOrg is the operator acting in one org: an owner's permissions, as no
// member. The org must exist.
func (s *Service) InOrg(ctx context.Context, a Actor, orgID uuid.UUID) (Actor, *model.Org, error) {
	if err := requireOperator(a); err != nil {
		return Actor{}, nil, err
	}
	o, err := s.store.Org(ctx, orgID)
	if err != nil {
		return Actor{}, nil, notFound(err, "org")
	}
	a.OrgID, a.Livemode, a.Role, a.OrgStatus = o.ID, true, model.RoleOwner, o.Status
	return a, o, nil
}

// Orgs.

// OperatedOrgInput creates an org from outside (ADR 0031).
type OperatedOrgInput struct {
	Name string
	// OwnerEmail is invited as the first owner.
	OwnerEmail  string
	ExternalRef string
	Limits      model.OrgLimits
}

// OrgChange changes what the operator sets on an org; nil leaves a field
// as it is.
type OrgChange struct {
	Name        *string
	Status      *model.OrgStatus
	StatusNote  *string
	ExternalRef *string
	Limits      *model.OrgLimits
}

func checkExternalRef(ref string, ps *apperr.Problems) {
	if len(ref) > 200 {
		ps.Add("external_ref_invalid", "external_ref", "An external reference is at most 200 characters.")
	}
}

func checkLimits(l model.OrgLimits, ps *apperr.Problems) {
	for name, v := range map[string]*int{"brands": l.Brands, "channels": l.Channels, "members": l.Members, "posts_per_month": l.PostsMonth} {
		if v != nil && *v < 0 {
			ps.Add("limit_invalid", "limits."+name, "A limit is a number from 0 up, or null for none.")
		}
	}
}

// CreateOperatedOrg creates an org with no members and invites its first
// owner, who sets their own password and two-factor authentication. It
// returns the invitation link to send them.
func (s *Service) CreateOperatedOrg(ctx context.Context, a Actor, in OperatedOrgInput) (*model.Org, string, *model.Invitation, error) {
	if err := requireOperator(a); err != nil {
		return nil, "", nil, err
	}
	var ps apperr.Problems
	in.Name, in.ExternalRef = strings.TrimSpace(in.Name), strings.TrimSpace(in.ExternalRef)
	if in.Name == "" || len(in.Name) > 100 {
		ps.Add("name_invalid", "name", "An org needs a name of 1 to 100 characters.")
	}
	norm, err := NormalizeEmail(in.OwnerEmail)
	if err != nil {
		ps.Add("email_invalid", "owner_email", "Give the first owner's email address.")
	}
	checkExternalRef(in.ExternalRef, &ps)
	checkLimits(in.Limits, &ps)
	if err := ps.Err("The org is not valid."); err != nil {
		return nil, "", nil, err
	}
	o := &model.Org{ID: id.New(), Name: in.Name, Status: model.OrgActive, ExternalRef: in.ExternalRef, Limits: in.Limits, CreatedAt: s.Now()}
	inOrg := a
	inOrg.OrgID, inOrg.Role, inOrg.OrgStatus = o.ID, model.RoleOwner, o.Status
	var link string
	var inv *model.Invitation
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateOrg(ctx, o); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("external_ref_taken", "Another org has the external reference %q.", in.ExternalRef)
			}
			return err
		}
		if err := s.audit(ctx, tx, inOrg, "org.create", id.Format(id.Org, o.ID), map[string]any{"name": o.Name, "external_ref": o.ExternalRef}); err != nil {
			return err
		}
		var err error
		link, inv, err = s.newInvitation(ctx, tx, inOrg, o.Name, in.OwnerEmail, norm, model.RoleOwner)
		return err
	})
	if err != nil {
		return nil, "", nil, err
	}
	return o, link, inv, nil
}

// OperatedOrgs lists the install's orgs, or the one with externalRef.
func (s *Service) OperatedOrgs(ctx context.Context, a Actor, p store.Page, externalRef string) ([]*model.Org, bool, error) {
	if err := requireOperator(a); err != nil {
		return nil, false, err
	}
	if externalRef != "" {
		o, err := s.store.OrgByExternalRef(ctx, externalRef)
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return []*model.Org{o}, false, nil
	}
	return s.store.Orgs(ctx, p)
}

// ChangeOrg changes an org's name, status, note, external reference or
// limits, as the operator acting in it (see InOrg).
func (s *Service) ChangeOrg(ctx context.Context, a Actor, ch OrgChange) (*model.Org, error) {
	if err := requireOperator(a); err != nil {
		return nil, err
	}
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return nil, notFound(err, "org")
	}
	var ps apperr.Problems
	changed := map[string]any{}
	if ch.Name != nil {
		if n := strings.TrimSpace(*ch.Name); n == "" || len(n) > 100 {
			ps.Add("name_invalid", "name", "An org needs a name of 1 to 100 characters.")
		} else if n != o.Name {
			o.Name, changed["name"] = n, n
		}
	}
	if ch.Status != nil {
		if !ch.Status.Valid() {
			ps.Add("status_invalid", "status", "Status is active, read_only or suspended.")
		} else if *ch.Status != o.Status {
			o.Status, changed["status"] = *ch.Status, *ch.Status
		}
	}
	if ch.StatusNote != nil {
		if n := strings.TrimSpace(*ch.StatusNote); len(n) > 500 {
			ps.Add("status_note_invalid", "status_note", "A status note is at most 500 characters.")
		} else if n != o.StatusNote {
			o.StatusNote, changed["status_note"] = n, n
		}
	}
	if ch.ExternalRef != nil {
		ref := strings.TrimSpace(*ch.ExternalRef)
		checkExternalRef(ref, &ps)
		if ref != o.ExternalRef {
			o.ExternalRef, changed["external_ref"] = ref, ref
		}
	}
	if ch.Limits != nil {
		checkLimits(*ch.Limits, &ps)
		o.Limits, changed["limits"] = *ch.Limits, *ch.Limits
	}
	if err := ps.Err("The change is not valid."); err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return o, nil
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetOrgOperated(ctx, o); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("external_ref_taken", "Another org has the external reference %q.", o.ExternalRef)
			}
			return notFound(err, "org")
		}
		return s.audit(ctx, tx, a, "org.operate", id.Format(id.Org, o.ID), changed)
	})
	return o, err
}

// InviteAsOperator invites someone to the org the operator acts in, with
// any role, and returns the link to send them.
func (s *Service) InviteAsOperator(ctx context.Context, a Actor, email string, role model.Role) (string, *model.Invitation, error) {
	if err := requireOperator(a); err != nil {
		return "", nil, err
	}
	return s.InviteMember(ctx, a, nil, email, role)
}

// Usage.

// MonthOf is the calendar month (UTC) holding t: its start and the next
// month's.
func MonthOf(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// Usage reads what the actor's org has now, and did in the month (UTC)
// holding at. Members who manage the org and the operator read it.
func (s *Service) Usage(ctx context.Context, a Actor, at time.Time) (*model.OrgUsage, error) {
	if !a.Operator && !a.Role.AtLeast(model.RoleAdmin) {
		return nil, apperr.Forbidden("Your role (%s) cannot see the org's usage.", a.Role)
	}
	start, end := MonthOf(at)
	return s.store.OrgUsage(ctx, a.OrgID, start, end, s.Now())
}

// Limits (ADR 0031).

// limit names what an org's limits cap.
type limit string

const (
	limitBrands   limit = "brands"
	limitChannels limit = "channels"
	limitMembers  limit = "members"
	limitPosts    limit = "posts_per_month"
)

var limitWords = map[limit]string{limitBrands: "brands", limitChannels: "live channels", limitMembers: "members and open invitations",
	limitPosts: "live posts this month"}

// checkLimit refuses adding `adding` more of what l counts when the org's
// limit would be passed. The operator is not held back. A race between two
// additions can pass a limit by one; limits are for plans, not security.
// exceptEmail leaves out an open invitation being replaced.
func (s *Service) checkLimit(ctx context.Context, a Actor, l limit, adding int, exceptEmail string) error {
	if a.Operator || adding <= 0 {
		return nil
	}
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return notFound(err, "org")
	}
	var most *int
	switch l {
	case limitBrands:
		most = o.Limits.Brands
	case limitChannels:
		most = o.Limits.Channels
	case limitMembers:
		most = o.Limits.Members
	case limitPosts:
		most = o.Limits.PostsMonth
	}
	if most == nil {
		return nil
	}
	var n int
	switch l {
	case limitBrands:
		n, err = s.store.CountBrands(ctx, o.ID)
	case limitChannels:
		n, err = s.store.CountLiveChannels(ctx, o.ID)
	case limitMembers:
		n, err = s.store.CountSeats(ctx, o.ID, s.Now(), exceptEmail)
	case limitPosts:
		start, _ := MonthOf(s.Now())
		n, err = s.store.CountLivePosts(ctx, o.ID, start)
	}
	if err != nil {
		return err
	}
	if n+adding > *most {
		return &apperr.Error{Kind: apperr.KindForbidden, Code: "limit_reached", Param: string(l),
			Message: fmt.Sprintf("This org has reached its limit of %d %s.", *most, limitWords[l])}
	}
	return nil
}

// Sign-up and billing links (ADR 0031).

// SignupURL is where people create an account and org, or "" when members
// create orgs themselves.
func (s *Service) SignupURL() string { return s.cfg.SignupURL }

// Billing reports whether owners are sent elsewhere to manage billing.
func (s *Service) Billing() bool { return s.cfg.BillingURL != "" && len(s.cfg.BillingLinkKey) > 0 }

// errOrgsElsewhere refuses creating an org where orgs come from sign-up.
func (s *Service) errOrgsElsewhere() error {
	return apperr.Forbidden("Orgs on this server are created at %s.", s.cfg.SignupURL)
}

// CreateOwnOrg makes an org with userID as its owner, as a signed-in
// person does from the dashboard, unless orgs come from sign-up elsewhere.
func (s *Service) CreateOwnOrg(ctx context.Context, userID uuid.UUID, name string) (*model.Org, error) {
	if s.cfg.SignupURL != "" {
		return nil, s.errOrgsElsewhere()
	}
	return s.CreateOrg(ctx, userID, name)
}

// BillingHandoffTTL is how long a billing hand-off can be used.
const BillingHandoffTTL = 5 * time.Minute

// BillingClaims are what a billing hand-off says (ADR 0031).
type BillingClaims struct {
	Org         string     `json:"org"`
	ExternalRef string     `json:"external_ref"`
	User        string     `json:"user"`
	Email       string     `json:"email"`
	Role        model.Role `json:"role"`
	IssuedAt    int64      `json:"iat"`
	Expires     int64      `json:"exp"`
}

// BillingLink returns the address an owner is sent to for billing, with a
// signed hand-off naming them and their org. Owners can always follow it,
// whatever the org's status: that is how a suspended org pays.
func (s *Service) BillingLink(ctx context.Context, a Actor) (string, error) {
	if !s.Billing() {
		return "", apperr.NotFound("billing page")
	}
	if a.UserID == nil || a.Role != model.RoleOwner {
		return "", apperr.Forbidden("Only owners manage billing.")
	}
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return "", notFound(err, "org")
	}
	u, err := s.store.User(ctx, *a.UserID)
	if err != nil {
		return "", err
	}
	now := s.Now()
	token, err := SignBillingHandoff(s.cfg.BillingLinkKey, BillingClaims{Org: id.Format(id.Org, o.ID), ExternalRef: o.ExternalRef,
		User: id.Format(id.User, u.ID), Email: u.Email, Role: a.Role, IssuedAt: now.Unix(), Expires: now.Add(BillingHandoffTTL).Unix()})
	if err != nil {
		return "", err
	}
	dest, err := url.Parse(s.cfg.BillingURL)
	if err != nil {
		return "", err
	}
	q := dest.Query()
	q.Set("token", token)
	dest.RawQuery = q.Encode()
	return dest.String(), nil
}

// SignBillingHandoff makes a hand-off token: v1.<payload>.<signature>,
// both base64url, the signature HMAC-SHA256 over "v1.<payload>".
func SignBillingHandoff(key []byte, c BillingClaims) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signed := "v1." + base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyBillingHandoff checks a hand-off token's signature and expiry at
// now, as the receiving service does; Araldo uses it in tests and as the
// reference.
func VerifyBillingHandoff(key []byte, token string, now time.Time) (*BillingClaims, error) {
	bad := errors.New("billing hand-off: invalid token")
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, bad
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, bad
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, bad
	}
	var c BillingClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, bad
	}
	if now.Unix() >= c.Expires {
		return nil, errors.New("billing hand-off: expired")
	}
	return &c, nil
}
