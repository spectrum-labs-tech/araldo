// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/utm"
)

// Orgs and members (ADR 0004).

// CreateOrg makes an org with userID as its owner.
func (s *Service) CreateOrg(ctx context.Context, userID uuid.UUID, name string) (*model.Org, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, apperr.Invalid("name_invalid", "name", "An org needs a name of 1 to 100 characters.")
	}
	o := &model.Org{ID: id.New(), Name: name}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateOrg(ctx, o); err != nil {
			return err
		}
		if err := tx.AddMember(ctx, &model.Membership{OrgID: o.ID, UserID: userID, Role: model.RoleOwner}); err != nil {
			return err
		}
		return s.audit(ctx, tx, Actor{OrgID: o.ID, UserID: &userID}, "org.create", id.Format(id.Org, o.ID), nil)
	})
	return o, err
}

// Org returns the actor's org.
func (s *Service) Org(ctx context.Context, a Actor) (*model.Org, error) {
	o, err := s.store.Org(ctx, a.OrgID)
	return o, notFound(err, "org")
}

// OperatorRequestID marks what the operator does through `araldo admin`.
const OperatorRequestID = "admin-cli"

// OperatorActor is the server's operator acting in an org through
// `araldo admin` (ADR 0028): with an owner's permissions, as no member.
// The org is an ID, a name, or empty when the install has only one.
func (s *Service) OperatorActor(ctx context.Context, org string, livemode bool, command string) (Actor, *model.Org, error) {
	var o *model.Org
	if u, err := id.Parse(id.Org, org); err == nil {
		if o, err = s.store.Org(ctx, u); err != nil {
			return Actor{}, nil, notFound(err, "org")
		}
	} else {
		found, err := s.store.FindOrgs(ctx, strings.TrimSpace(org), 10)
		switch {
		case err != nil:
			return Actor{}, nil, err
		case len(found) == 0 && org == "":
			return Actor{}, nil, apperr.NotFound("org")
		case len(found) == 0:
			return Actor{}, nil, apperr.Invalid("org_not_found", "org", "No org is named %q.", org)
		case len(found) > 1 && org == "":
			return Actor{}, nil, apperr.Invalid("org_required", "org", "This server has more than one org: name one with --org.")
		case len(found) > 1:
			return Actor{}, nil, apperr.Invalid("org_ambiguous", "org", "More than one org is named %q: give its ID (%s, %s, …).",
				org, id.Format(id.Org, found[0].ID), id.Format(id.Org, found[1].ID))
		}
		o = found[0]
	}
	return Actor{OrgID: o.ID, Livemode: livemode, Role: model.RoleOwner, Operator: true, OperatorCommand: command,
		RequestID: OperatorRequestID}, o, nil
}

// UserOrgs lists the orgs a user belongs to.
func (s *Service) UserOrgs(ctx context.Context, userID uuid.UUID) ([]model.Membership, error) {
	return s.store.UserMemberships(ctx, userID)
}

// UpdateOrg changes an org's name and MFA policy.
//
// Turning off required two-factor authentication needs a recent
// re-authentication (ADR 0007).
func (s *Service) UpdateOrg(ctx context.Context, a Actor, ss *model.Session, name string, requireMFA bool) error {
	if err := a.require(PermOrgWrite); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return apperr.Invalid("name_invalid", "name", "An org needs a name of 1 to 100 characters.")
	}
	if requireMFA && a.UserID != nil {
		u, err := s.store.User(ctx, *a.UserID)
		if err != nil {
			return err
		}
		if !u.MFAEnabled() {
			return apperr.Invalid("mfa_required_first", "require_mfa", "Turn on two-factor authentication for yourself before requiring it.")
		}
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		o, err := tx.Org(ctx, a.OrgID)
		if err != nil {
			return err
		}
		if o.RequireMFA && !requireMFA {
			if err := s.requireSudoFor(a, ss); err != nil {
				return err
			}
		}
		if err := tx.UpdateOrg(ctx, &model.Org{ID: a.OrgID, Name: name, RequireMFA: requireMFA}); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "org.update", id.Format(id.Org, a.OrgID), map[string]any{"require_mfa": requireMFA})
	})
}

// Members lists an org's members.
func (s *Service) Members(ctx context.Context, a Actor) ([]model.Membership, error) {
	if a.IsKey() {
		return nil, apperr.Forbidden("API keys cannot list members.")
	}
	return s.store.Members(ctx, a.OrgID)
}

// Member returns one member of the org; any member may look.
func (s *Service) Member(ctx context.Context, a Actor, userID uuid.UUID) (*model.Membership, error) {
	if a.IsKey() {
		return nil, apperr.Forbidden("API keys cannot read members.")
	}
	m, err := s.store.Member(ctx, a.OrgID, userID)
	return m, notFound(err, "member")
}

// AddMember adds a person to the org. Someone without an account gets one
// with the given temporary password, to share with them out of band.
// Adding an owner needs a recent re-authentication (ADR 0007).
func (s *Service) AddMember(ctx context.Context, a Actor, ss *model.Session, email string, role model.Role, tempPassword string) (*model.User, error) {
	if err := a.require(PermMembersWrite); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, apperr.Invalid("role_invalid", "role", "Role must be owner, admin, editor or viewer.")
	}
	if role == model.RoleOwner {
		if !a.Can(PermOrgWrite) {
			return nil, apperr.Forbidden("Only owners can add owners.")
		}
		if err := s.requireSudoFor(a, ss); err != nil {
			return nil, err
		}
	}
	u, err := s.UserByEmail(ctx, email)
	if errors.Is(err, apperr.ErrNotFound) {
		if tempPassword == "" {
			return nil, apperr.Invalid("password_required", "password", "%s has no account yet: set a temporary password for them.", email)
		}
		u, err = s.CreateUser(ctx, email, "", tempPassword)
	}
	if err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.AddMember(ctx, &model.Membership{OrgID: a.OrgID, UserID: u.ID, Role: role}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("already_member", "%s is already a member.", u.Email)
			}
			return err
		}
		return s.audit(ctx, tx, a, "member.add", id.Format(id.User, u.ID), map[string]any{"role": role})
	})
	return u, err
}

// SetMemberRole changes a member's role. Owners are managed by owners, with
// a recent re-authentication (ADR 0007), and an org always keeps at least
// one.
func (s *Service) SetMemberRole(ctx context.Context, a Actor, ss *model.Session, userID uuid.UUID, role model.Role) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	if !role.Valid() {
		return apperr.Invalid("role_invalid", "role", "Role must be owner, admin, editor or viewer.")
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		m, err := tx.Membership(ctx, a.OrgID, userID)
		if err != nil {
			return notFound(err, "member")
		}
		if m.Role == model.RoleOwner || role == model.RoleOwner {
			if !a.Can(PermOrgWrite) {
				return apperr.Forbidden("Only owners can change owners.")
			}
			if err := s.requireSudoFor(a, ss); err != nil {
				return err
			}
		}
		if m.Role == model.RoleOwner && role != model.RoleOwner {
			if err := s.keepAnOwner(ctx, tx, a.OrgID); err != nil {
				return err
			}
		}
		if err := tx.SetMemberRole(ctx, a.OrgID, userID, role); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "member.role", id.Format(id.User, userID), map[string]any{"role": role})
	})
}

// RemoveMember takes a person out of the org. Removing an owner needs a
// recent re-authentication (ADR 0007).
func (s *Service) RemoveMember(ctx context.Context, a Actor, ss *model.Session, userID uuid.UUID) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		m, err := tx.Membership(ctx, a.OrgID, userID)
		if err != nil {
			return notFound(err, "member")
		}
		if m.Role == model.RoleOwner {
			if !a.Can(PermOrgWrite) {
				return apperr.Forbidden("Only owners can remove owners.")
			}
			if err := s.requireSudoFor(a, ss); err != nil {
				return err
			}
			if err := s.keepAnOwner(ctx, tx, a.OrgID); err != nil {
				return err
			}
		}
		if err := tx.RemoveMember(ctx, a.OrgID, userID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "member.remove", id.Format(id.User, userID), nil)
	})
}

func (s *Service) keepAnOwner(ctx context.Context, tx *store.Store, orgID uuid.UUID) error {
	n, err := tx.CountOwners(ctx, orgID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return apperr.Conflict("last_owner", "An org must keep at least one owner.")
	}
	return nil
}

// Brands.

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// BrandInput creates or changes a brand.
type BrandInput struct {
	Name           string
	Slug           string
	Timezone       string
	ApprovalPolicy model.ApprovalPolicy
	UTMDomains     []string
	// Slots replaces the weekly slots when not nil. A new brand without
	// them gets weekdays at 09:00 and 13:00.
	Slots *[]model.Slot
}

func (in *BrandInput) check() error {
	var ps apperr.Problems
	if in.Slots != nil {
		checkSlots(*in.Slots, &ps)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		ps.Add("name_invalid", "name", "A brand needs a name of 1 to 100 characters.")
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	}
	if !slugRE.MatchString(in.Slug) {
		ps.Add("slug_invalid", "slug", "Slugs use lowercase letters, digits and dashes.")
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		ps.Add("timezone_invalid", "timezone", "%q is not an IANA time zone.", in.Timezone)
	}
	if in.ApprovalPolicy == "" {
		in.ApprovalPolicy = model.ApprovalNone
	}
	if !in.ApprovalPolicy.Valid() {
		ps.Add("approval_policy_invalid", "approval_policy", "Approval policy must be none, required_for_editors_and_keys or required_for_all.")
	}
	domains, err := utm.NormalizeDomains(in.UTMDomains)
	var de *utm.DomainError
	switch {
	case errors.As(err, &de):
		ps.Add("utm_domain_invalid", "utm_domains", "%q is not a domain, like example.com.", de.Domain)
	case len(domains) > utm.MaxDomains:
		ps.Add("utm_domains_too_many", "utm_domains", "A brand can tag links to at most %d domains.", utm.MaxDomains)
	default:
		in.UTMDomains = domains
	}
	return ps.Err("The brand is not valid.")
}

// checkSlots adds a problem for each invalid slot.
func checkSlots(slots []model.Slot, ps *apperr.Problems) {
	for i, sl := range slots {
		if sl.Weekday < time.Sunday || sl.Weekday > time.Saturday || sl.MinuteOfDay < 0 || sl.MinuteOfDay >= 24*60 {
			ps.Add("slot_invalid", "slots["+strconv.Itoa(i)+"]", "Slots need a weekday and a time of day.")
		}
	}
	if len(slots) > 200 {
		ps.Add("too_many_slots", "slots", "A brand can have at most 200 weekly slots.")
	}
}

// Slugify makes a slug from a name.
func Slugify(name string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			sb.WriteRune(r)
			dash = false
		case !dash && sb.Len() > 0:
			sb.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(sb.String(), "-")
}

// CreateBrand adds a brand, with default weekday slots at 09:00 and 13:00.
func (s *Service) CreateBrand(ctx context.Context, a Actor, in BrandInput) (*model.Brand, error) {
	if err := a.require(PermBrandsWrite); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		return nil, apperr.Forbidden("This API key is limited to one brand.")
	}
	if err := in.check(); err != nil {
		return nil, err
	}
	b := &model.Brand{ID: id.New(), OrgID: a.OrgID, Name: in.Name, Slug: in.Slug, Timezone: in.Timezone, ApprovalPolicy: in.ApprovalPolicy,
		UTMDomains: in.UTMDomains}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateBrand(ctx, b); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("slug_taken", "slug", "Another brand already uses the slug %q.", b.Slug)
			}
			return err
		}
		var slots []model.Slot
		if in.Slots != nil {
			slots = *in.Slots
		} else {
			for d := time.Monday; d <= time.Friday; d++ {
				slots = append(slots, model.Slot{Weekday: d, MinuteOfDay: 9 * 60}, model.Slot{Weekday: d, MinuteOfDay: 13 * 60})
			}
		}
		if err := tx.ReplaceSlots(ctx, a.OrgID, b.ID, slots); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "brand.create", id.Format(id.Brand, b.ID), map[string]any{"name": b.Name}); err != nil {
			return err
		}
		var err error
		b.Slots, err = tx.Slots(ctx, a.OrgID, b.ID)
		return err
	})
	return b, err
}

// UpdateBrand changes a brand.
func (s *Service) UpdateBrand(ctx context.Context, a Actor, brandID uuid.UUID, in BrandInput) (*model.Brand, error) {
	if err := a.require(PermBrandsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, brandID)
	if err != nil {
		return nil, err
	}
	if in.Slug == "" {
		in.Slug = b.Slug
	}
	if err := in.check(); err != nil {
		return nil, err
	}
	b.Name, b.Slug, b.Timezone, b.ApprovalPolicy, b.UTMDomains = in.Name, in.Slug, in.Timezone, in.ApprovalPolicy, in.UTMDomains
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateBrand(ctx, b); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("slug_taken", "slug", "Another brand already uses the slug %q.", b.Slug)
			}
			return err
		}
		if in.Slots != nil {
			if err := tx.ReplaceSlots(ctx, a.OrgID, b.ID, *in.Slots); err != nil {
				return err
			}
			if b.Slots, err = tx.Slots(ctx, a.OrgID, b.ID); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, a, "brand.update", id.Format(id.Brand, b.ID), nil)
	})
	return b, err
}

// Brand returns one of the actor's brands.
func (s *Service) Brand(ctx context.Context, a Actor, brandID uuid.UUID) (*model.Brand, error) {
	if err := a.require(PermBrandsRead); err != nil && !a.Can(PermPostsRead) && !a.Can(PermPostsWrite) && !a.Can(PermAdsRead) && !a.Can(PermAdsWrite) &&
		!a.Can(PermNewslettersRead) && !a.Can(PermNewslettersWrite) {
		return nil, err
	}
	if err := a.brandAllowed(brandID); err != nil {
		return nil, err
	}
	b, err := s.store.Brand(ctx, a.OrgID, brandID)
	return b, notFound(err, "brand")
}

// Brands lists the actor's brands.
func (s *Service) Brands(ctx context.Context, a Actor) ([]*model.Brand, error) {
	if err := a.require(PermBrandsRead); err != nil && !a.Can(PermPostsRead) && !a.Can(PermPostsWrite) && !a.Can(PermAdsRead) && !a.Can(PermAdsWrite) &&
		!a.Can(PermNewslettersRead) && !a.Can(PermNewslettersWrite) {
		return nil, err
	}
	bs, err := s.store.Brands(ctx, a.OrgID)
	if err != nil || a.BrandID == nil {
		return bs, err
	}
	return slices.DeleteFunc(bs, func(b *model.Brand) bool { return b.ID != *a.BrandID }), nil
}

// Slots returns a brand's weekly slots.
func (s *Service) Slots(ctx context.Context, a Actor, brandID uuid.UUID) ([]model.Slot, error) {
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return nil, err
	}
	return s.store.Slots(ctx, a.OrgID, brandID)
}

// SetSlots replaces a brand's weekly slots.
func (s *Service) SetSlots(ctx context.Context, a Actor, brandID uuid.UUID, slots []model.Slot) error {
	if err := a.require(PermBrandsWrite); err != nil {
		return err
	}
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return err
	}
	var ps apperr.Problems
	checkSlots(slots, &ps)
	if err := ps.Err("The slots are not valid."); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.ReplaceSlots(ctx, a.OrgID, brandID, slots); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "brand.slots", id.Format(id.Brand, brandID), map[string]any{"count": len(slots)})
	})
}

// API keys (ADR 0006).

// APIKeyInput creates a key.
type APIKeyInput struct {
	Name     string
	Livemode bool
	Scopes   []string
	BrandID  *uuid.UUID
	Expires  *time.Time
	// createdBy carries a rolled key's creator over to its replacement.
	createdBy *uuid.UUID
	// self marks a key replacing itself: it keeps what it has.
	self bool
}

// CreateAPIKey makes a key and returns it in full, the only time it is
// shown. It needs a recent re-authentication.
func (s *Service) CreateAPIKey(ctx context.Context, a Actor, ss *model.Session, in APIKeyInput) (string, *model.APIKey, error) {
	if err := a.require(PermKeysWrite); err != nil {
		return "", nil, err
	}
	if err := s.requireSudo(ss); err != nil {
		return "", nil, err
	}
	return s.createAPIKey(ctx, a, in)
}

// CreateOperatorAPIKey makes a key for the `araldo admin apikeys create`
// command. Whoever runs it already holds the database URL and master keys,
// so the dashboard's re-authentication adds nothing; the member still needs
// permission to manage keys.
func (s *Service) CreateOperatorAPIKey(ctx context.Context, a Actor, in APIKeyInput) (string, *model.APIKey, error) {
	if err := a.require(PermKeysWrite); err != nil {
		return "", nil, err
	}
	return s.createAPIKey(ctx, a, in)
}

func (s *Service) createAPIKey(ctx context.Context, a Actor, in APIKeyInput) (string, *model.APIKey, error) {
	var ps apperr.Problems
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		ps.Add("name_invalid", "name", "A key needs a name of 1 to 100 characters.")
	}
	for _, sc := range in.Scopes {
		if !slices.Contains(KeyScopes, Permission(sc)) && !slices.Contains(AdminScopes, Permission(sc)) {
			ps.Add("scope_invalid", "scopes", "Unknown scope %q.", sc)
		}
	}
	if in.BrandID != nil {
		if _, err := s.store.Brand(ctx, a.OrgID, *in.BrandID); err != nil {
			ps.Add("brand_invalid", "brand", "No such brand.")
		}
	}
	if a.IsKey() && !in.self {
		checkGrant(a, in, &ps)
	}
	if err := ps.Err("The key is not valid."); err != nil {
		return "", nil, err
	}
	plain := authn.NewAPIKey(in.Livemode)
	createdBy := a.UserID
	if in.createdBy != nil {
		createdBy = in.createdBy
	}
	k := &model.APIKey{ID: id.New(), OrgID: a.OrgID, Livemode: in.Livemode, Name: in.Name, Hint: authn.KeyHint(plain),
		Scopes: in.Scopes, BrandID: in.BrandID, CreatedBy: createdBy, ExpiresAt: in.Expires}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateAPIKey(ctx, k, authn.HashToken(plain)); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "api_key.create", id.Format(id.APIKey, k.ID), map[string]any{"livemode": in.Livemode, "scopes": in.Scopes})
	})
	return plain, k, err
}

// checkGrant applies ADR 0019's rule to a key creating a key: only in its
// own mode, for its own brand if it has one, with scopes it holds itself,
// never an admin scope, and expiring no later than itself.
func checkGrant(a Actor, in APIKeyInput, ps *apperr.Problems) {
	if a.KeyExpiresAt != nil && (in.Expires == nil || in.Expires.After(*a.KeyExpiresAt)) {
		ps.Add("expires_too_late", "expires_at", "This key expires at %s, so the keys it creates must expire by then.",
			a.KeyExpiresAt.UTC().Format(time.RFC3339))
	}
	if in.Livemode != a.Livemode {
		ps.Add("livemode_mismatch", "livemode", "A key can only create keys in its own mode.")
	}
	if a.BrandID != nil && (in.BrandID == nil || *in.BrandID != *a.BrandID) {
		ps.Add("brand_required", "brand", "This key is limited to one brand, so the keys it creates must be too.")
	}
	scopes := in.Scopes
	if len(scopes) == 0 {
		for _, p := range KeyScopes {
			scopes = append(scopes, string(p))
		}
	}
	for _, sc := range scopes {
		switch {
		case slices.Contains(AdminScopes, Permission(sc)):
			ps.Add("scope_not_grantable", "scopes", "Only a member can create a key with %s.", sc)
		case !a.Can(Permission(sc)):
			ps.Add("scope_not_held", "scopes", "This key cannot grant %s, which it does not hold.", sc)
		}
	}
}

// keyVisible reports whether a key caller may see or act on k: same mode,
// and its own brand if it is limited to one.
func keyVisible(a Actor, k *model.APIKey) bool {
	if !a.IsKey() {
		return true
	}
	return k.Livemode == a.Livemode && (a.BrandID == nil || k.BrandID != nil && *k.BrandID == *a.BrandID)
}

// APIKeys lists the org's keys (a key sees those of its mode and brand).
func (s *Service) APIKeys(ctx context.Context, a Actor) ([]*model.APIKey, error) {
	if err := a.require(PermKeysWrite); err != nil {
		return nil, err
	}
	all, err := s.store.APIKeys(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, k := range all {
		if keyVisible(a, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// APIKey returns one key's definition, never its secret. Any member may
// look (it shows who or what made a post); a key needs keys:write.
func (s *Service) APIKey(ctx context.Context, a Actor, keyID uuid.UUID) (*model.APIKey, error) {
	if a.IsKey() {
		if err := a.require(PermKeysWrite); err != nil {
			return nil, err
		}
	}
	k, err := s.store.APIKey(ctx, a.OrgID, keyID)
	if err != nil || !keyVisible(a, k) {
		return nil, apperr.NotFound("API key")
	}
	return k, nil
}

// CreateKeyWithKey is CreateAPIKey for an API key with keys:write: no
// session to re-authenticate, and the grant rule applies (ADR 0019).
func (s *Service) CreateKeyWithKey(ctx context.Context, a Actor, in APIKeyInput) (string, *model.APIKey, error) {
	if err := a.require(PermKeysWrite); err != nil {
		return "", nil, err
	}
	if !a.IsKey() {
		return "", nil, apperr.Forbidden("Members create keys in the dashboard.")
	}
	in.Livemode = a.Livemode
	return s.createAPIKey(ctx, a, in)
}

// RevokeAPIKey stops a key working immediately.
func (s *Service) RevokeAPIKey(ctx context.Context, a Actor, keyID uuid.UUID) error {
	if err := a.require(PermKeysWrite); err != nil {
		return err
	}
	if a.IsKey() {
		k, err := s.store.APIKey(ctx, a.OrgID, keyID)
		if err != nil || !keyVisible(a, k) {
			return apperr.NotFound("API key")
		}
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.ExpireAPIKey(ctx, a.OrgID, keyID, s.Now(), true); err != nil {
			return notFound(err, "API key")
		}
		return s.audit(ctx, tx, a, "api_key.revoke", id.Format(id.APIKey, keyID), nil)
	})
}

// RollAPIKey issues a replacement key with the same settings and lets the
// old one keep working for overlap (at most 7 days). A member needs a recent
// re-authentication; a key may roll only keys it could create.
func (s *Service) RollAPIKey(ctx context.Context, a Actor, ss *model.Session, keyID uuid.UUID, overlap time.Duration) (string, *model.APIKey, error) {
	if err := a.require(PermKeysWrite); err != nil {
		return "", nil, err
	}
	if !a.IsKey() {
		if err := s.requireSudo(ss); err != nil {
			return "", nil, err
		}
	}
	return s.rollKey(ctx, a, keyID, overlap, false)
}

// RollOwnKey replaces the calling key's secret; the old one keeps working
// for overlap. Any key may: it gains nothing it did not have.
func (s *Service) RollOwnKey(ctx context.Context, a Actor, overlap time.Duration) (string, *model.APIKey, error) {
	if !a.IsKey() {
		return "", nil, apperr.Forbidden("Only an API key can roll itself.")
	}
	return s.rollKey(ctx, a, *a.KeyID, overlap, true)
}

// rollKey creates the replacement and expires the old key after overlap.
// self skips the grant rule: a key replacing itself keeps what it has.
func (s *Service) rollKey(ctx context.Context, a Actor, keyID uuid.UUID, overlap time.Duration, self bool) (string, *model.APIKey, error) {
	old, err := s.store.APIKey(ctx, a.OrgID, keyID)
	if err != nil || !keyVisible(a, old) {
		return "", nil, apperr.NotFound("API key")
	}
	if !old.Active(s.Now()) {
		return "", nil, apperr.Conflict("key_inactive", "That key is no longer active.")
	}
	overlap = min(max(overlap, 0), 7*24*time.Hour)
	in := APIKeyInput{Name: old.Name, Livemode: old.Livemode, Scopes: old.Scopes, BrandID: old.BrandID, createdBy: old.CreatedBy, self: self}
	if old.ExpiresAt != nil {
		// The new key lasts as long as the old one was meant to.
		in.Expires = ptr(s.Now().Add(old.ExpiresAt.Sub(old.CreatedAt)))
	}
	if a.KeyExpiresAt != nil && (in.Expires == nil || in.Expires.After(*a.KeyExpiresAt)) {
		// But a key never makes one that outlives it, so a key rolling
		// itself keeps its expiry: rolling cannot keep a leaked key alive.
		// Extending a key's life is for a member, in the dashboard.
		in.Expires = a.KeyExpiresAt
	}
	plain, k, err := s.createAPIKey(ctx, a, in)
	if err != nil {
		return "", nil, err
	}
	if err := s.store.ExpireAPIKey(ctx, a.OrgID, keyID, s.Now().Add(overlap), false); err != nil {
		return "", nil, err
	}
	return plain, k, nil
}

// AuthenticateKey turns an API key into an actor.
func (s *Service) AuthenticateKey(ctx context.Context, plain, requestID string) (Actor, error) {
	bad := &apperr.Error{Kind: apperr.KindUnauthorized, Code: "api_key_invalid", Message: "Invalid API key. Keys look like ald_test_… or ald_live_…"}
	if _, err := authn.ParseAPIKey(plain); err != nil {
		return Actor{}, bad
	}
	k, err := s.store.APIKeyByHash(ctx, authn.HashToken(plain))
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, bad
	}
	if err != nil {
		return Actor{}, err
	}
	now := s.Now()
	if !k.Active(now) {
		return Actor{}, &apperr.Error{Kind: apperr.KindUnauthorized, Code: "api_key_expired", Message: "This API key was revoked or has expired."}
	}
	_ = s.store.TouchAPIKey(ctx, k.ID, now)
	return Actor{OrgID: k.OrgID, Livemode: k.Livemode, KeyID: &k.ID, Scopes: k.Scopes, BrandID: k.BrandID, KeyExpiresAt: k.ExpiresAt,
		RequestID: requestID}, nil
}

// MeView is what a credential can learn about itself: the org and mode it
// acts in, and the API key or member it is (ADR 0028). Clients use it to
// show who they are signed in as.
type MeView struct {
	Object   string      `json:"object"`
	Livemode bool        `json:"livemode"`
	Org      MeOrgView   `json:"org"`
	APIKey   *APIKeyView `json:"api_key,omitempty"`
	// User and UserToken are set for a person signed in with a user token.
	User      *MeUserView    `json:"user,omitempty"`
	UserToken *UserTokenView `json:"user_token,omitempty"`
	// Role is the person's role in the org.
	Role model.Role `json:"role,omitempty"`
}

// MeUserView is the person a user token acts as.
type MeUserView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// UserTokenView is a user token, never its secret but once, when issued.
type UserTokenView struct {
	ID         string     `json:"id"`
	Object     string     `json:"object"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	Livemode   bool       `json:"livemode"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ViewUserToken renders a user token.
func ViewUserToken(t *model.UserToken) UserTokenView {
	return UserTokenView{ID: id.Format(id.UserToken, t.ID), Object: "user_token", Name: t.Name, Hint: t.Hint, Livemode: t.Livemode,
		CreatedAt: t.CreatedAt.UTC(), LastUsedAt: utc(t.LastUsedAt)}
}

// MeOrgView is the org a credential acts in.
type MeOrgView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Me describes the actor's own credential. Every credential may read
// itself, whatever its scopes.
func (s *Service) Me(ctx context.Context, a Actor) (MeView, error) {
	o, err := s.store.Org(ctx, a.OrgID)
	if err != nil {
		return MeView{}, notFound(err, "org")
	}
	v := MeView{Object: "me", Livemode: a.Livemode, Org: MeOrgView{ID: id.Format(id.Org, o.ID), Name: o.Name}}
	if a.KeyID != nil {
		k, err := s.store.APIKey(ctx, a.OrgID, *a.KeyID)
		if err != nil {
			return MeView{}, notFound(err, "API key")
		}
		kv := ViewAPIKey(k)
		v.APIKey = &kv
	}
	if a.TokenID != nil && a.UserID != nil {
		u, err := s.store.User(ctx, *a.UserID)
		if err != nil {
			return MeView{}, err
		}
		v.User = &MeUserView{ID: id.Format(id.User, u.ID), Email: u.Email, Name: u.Name}
		v.Role = a.Role
		ts, err := s.store.UserTokens(ctx, u.ID)
		if err != nil {
			return MeView{}, err
		}
		for _, t := range ts {
			if t.ID == *a.TokenID {
				tv := ViewUserToken(t)
				v.UserToken = &tv
			}
		}
	}
	return v, nil
}
