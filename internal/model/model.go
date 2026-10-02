// SPDX-License-Identifier: AGPL-3.0-or-later

// Package model holds Araldo's domain types. It has no storage or transport
// concerns: no struct tags for SQL, no HTTP.
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Role is a member's role in an org (ADR 0004).
type Role string

// Roles, most powerful first.
const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleEditor || r == RoleViewer
}

// AtLeast reports whether r is at least as powerful as min.
func (r Role) AtLeast(min Role) bool { return rank(r) >= rank(min) }

func rank(r Role) int {
	switch r {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// User is a person who signs in.
type User struct {
	ID            uuid.UUID
	Email         string
	Name          string
	PasswordHash  string
	TOTPSecret    []byte // encrypted (ADR 0008)
	TOTPLastStep  int64
	TOTPEnabledAt *time.Time
	FailedLogins  int
	LockedUntil   *time.Time
	CreatedAt     time.Time
}

// MFAEnabled reports whether the user has a second factor.
func (u *User) MFAEnabled() bool { return u.TOTPEnabledAt != nil }

// SessionState is how far sign-in has got.
type SessionState string

// Session states.
const (
	SessionPendingMFA SessionState = "pending_mfa"
	SessionActive     SessionState = "active"
)

// Session is a signed-in browser.
type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	CSRFToken  string
	State      SessionState
	CurrentOrg *uuid.UUID
	Livemode   bool
	SudoUntil  *time.Time
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Org is a tenant.
type Org struct {
	ID         uuid.UUID
	Name       string
	RequireMFA bool
	CreatedAt  time.Time
}

// Membership is a user's role in an org.
type Membership struct {
	OrgID     uuid.UUID
	UserID    uuid.UUID
	Role      Role
	CreatedAt time.Time
	// Filled by listings.
	OrgName   string
	UserEmail string
	UserName  string
	UserMFA   bool
}

// ApprovalPolicy decides which posts wait for review (ADR 0004).
type ApprovalPolicy string

// Approval policies.
const (
	ApprovalNone          ApprovalPolicy = "none"
	ApprovalEditorsAndKey ApprovalPolicy = "required_for_editors_and_keys"
	ApprovalAll           ApprovalPolicy = "required_for_all"
)

// Valid reports whether p is known.
func (p ApprovalPolicy) Valid() bool {
	return p == ApprovalNone || p == ApprovalEditorsAndKey || p == ApprovalAll
}

// Brand is a product or voice within an org.
type Brand struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	Name           string
	Slug           string
	Timezone       string
	ApprovalPolicy ApprovalPolicy
	// UTMDomains are the sites whose links get UTM parameters (empty: none).
	UTMDomains []string
	CreatedAt  time.Time
	// Slots are its weekly publishing times, filled by reads.
	Slots []Slot
}

// Slot is a weekly publishing time in the brand's time zone.
type Slot struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	BrandID     uuid.UUID
	Weekday     time.Weekday
	MinuteOfDay int
}

// APIKey is a stored API key (only its hash).
type APIKey struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Livemode   bool
	Name       string
	Hint       string
	Scopes     []string
	BrandID    *uuid.UUID
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// Active reports whether the key can be used at now.
func (k *APIKey) Active(now time.Time) bool {
	return k.RevokedAt == nil && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

// ChannelStatus is whether a channel can publish.
type ChannelStatus string

// Channel statuses.
const (
	ChannelActive      ChannelStatus = "active"
	ChannelNeedsReauth ChannelStatus = "needs_reauth"
	ChannelDisabled    ChannelStatus = "disabled"
)

// Channel is a connected account under a brand.
type Channel struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	BrandID     uuid.UUID
	Livemode    bool
	Provider    platform.Provider
	Emulates    platform.Provider // sandbox only
	DisplayName string
	Handle      string
	ExternalID  string
	ProfileURL  string
	Settings    map[string]string // non-secret fields
	Credentials []byte            // encrypted secret fields
	Status      ChannelStatus
	StatusNote  string
	HoldUntil   *time.Time
	CreatedAt   time.Time
	// AppID is the developer app an OAuth channel connected through, and
	// TokenExpiresAt when its token expires (ADR 0021).
	AppID          *uuid.UUID
	TokenExpiresAt *time.Time
}

// ProviderApp is an org's developer app on a platform (ADR 0021).
type ProviderApp struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	Provider platform.Provider
	Name     string
	ClientID string
	// ClientSecret is encrypted (ADR 0008).
	ClientSecret []byte
	CreatedBy    *uuid.UUID
	CreatedAt    time.Time
}

// OAuthState is a sign-in in progress (ADR 0021).
type OAuthState struct {
	OrgID    uuid.UUID
	UserID   uuid.UUID
	BrandID  uuid.UUID
	AppID    uuid.UUID
	Provider platform.Provider
	Verifier string
	// Connections, encrypted, wait for the member to choose among them.
	Connections []byte
	CreatedAt   time.Time
}

// RulesProvider is the platform whose rules apply to the channel.
func (c *Channel) RulesProvider() platform.Provider {
	if c.Provider == platform.Sandbox {
		return c.Emulates
	}
	return c.Provider
}

// TemplateApproval overrides the brand's approval policy for posts made
// from a template (ADR 0004).
type TemplateApproval string

// Template approval settings.
const (
	TemplateApprovalInherit     TemplateApproval = "inherit"
	TemplateApprovalRequired    TemplateApproval = "required"
	TemplateApprovalNotRequired TemplateApproval = "not_required"
)

// Valid reports whether a is known.
func (a TemplateApproval) Valid() bool {
	return a == TemplateApprovalInherit || a == TemplateApprovalRequired || a == TemplateApprovalNotRequired
}

// Template is a named, versioned template in a brand.
type Template struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	BrandID       uuid.UUID
	Key           string
	Name          string
	Approval      TemplateApproval
	LatestVersion int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TemplateVersion is one immutable version of a template.
type TemplateVersion struct {
	OrgID      uuid.UUID
	TemplateID uuid.UUID
	Version    int
	Variables  json.RawMessage
	Examples   []json.RawMessage
	Body       string
	Overrides  map[platform.Provider]string
	Fit        map[platform.Provider]platform.Fit
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
}

// PostStatus summarizes a post's targets (ADR 0011).
type PostStatus string

// Post statuses.
const (
	PostPendingApproval    PostStatus = "pending_approval"
	PostScheduled          PostStatus = "scheduled"
	PostPublishing         PostStatus = "publishing"
	PostPublished          PostStatus = "published"
	PostPartiallyPublished PostStatus = "partially_published"
	PostFailed             PostStatus = "failed"
	PostCanceled           PostStatus = "canceled"
	PostRejected           PostStatus = "rejected"
)

// Content is text written by the caller instead of a template.
type Content struct {
	Body      string                             `json:"body,omitempty"`
	Parts     []string                           `json:"parts,omitempty"`
	Overrides map[platform.Provider]string       `json:"overrides,omitempty"`
	Fit       map[platform.Provider]platform.Fit `json:"fit,omitempty"`
}

// Post is something to publish to one or more channels.
type Post struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	BrandID         uuid.UUID
	Livemode        bool
	Status          PostStatus
	TemplateID      *uuid.UUID
	TemplateVersion *int
	Data            json.RawMessage
	Content         *Content
	PublishAt       time.Time
	PublishBy       time.Time
	SlotAt          *time.Time
	Metadata        map[string]string
	ApprovalNeeded  bool
	ReviewedBy      *uuid.UUID
	ReviewedByKey   *uuid.UUID
	ReviewedAt      *time.Time
	ReviewNote      string
	CreatedByUser   *uuid.UUID
	CreatedByKey    *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Targets         []Target
	// Media are attached in this order (ADR 0017).
	Media []*Media
}

// Media storage backends (ADR 0017).
const (
	StoragePostgres = "postgres"
	StorageS3       = "s3"
)

// Media is an uploaded image (ADR 0017). The file never changes; Alt can.
type Media struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	BrandID       uuid.UUID
	Livemode      bool
	ContentType   string
	Size          int64
	Width, Height int
	SHA256        []byte
	Alt           string
	Filename      string
	// Storage is where the file is: StoragePostgres, or StorageS3 at
	// StorageKey.
	Storage       string
	StorageKey    string
	CreatedByUser *uuid.UUID
	CreatedByKey  *uuid.UUID
	CreatedAt     time.Time
}

// TargetStatus is where one channel's copy of a post stands (ADR 0011).
type TargetStatus string

// Target statuses.
const (
	TargetHeld           TargetStatus = "held" // waiting for approval
	TargetQueued         TargetStatus = "queued"
	TargetPublishing     TargetStatus = "publishing"
	TargetPublished      TargetStatus = "published"
	TargetFailed         TargetStatus = "failed"
	TargetNeedsAttention TargetStatus = "needs_attention"
	TargetCanceled       TargetStatus = "canceled"
)

// Final reports whether no more work will happen on the target.
func (s TargetStatus) Final() bool {
	return s == TargetPublished || s == TargetFailed || s == TargetCanceled
}

// Target is one channel's copy of a post: the unit of publishing.
type Target struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	PostID        uuid.UUID
	ChannelID     uuid.UUID
	Livemode      bool
	Provider      platform.Provider
	Parts         []string
	Status        TargetStatus
	Attempts      int
	NextAttemptAt time.Time
	PublishBy     time.Time
	LeaseOwner    string
	LeaseUntil    *time.Time
	Posted        []platform.RemoteRef
	Permalink     string
	ErrorCode     string
	ErrorMessage  string
	PublishedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Filled by some reads.
	ChannelName string
	// Engagement is the latest reading, once the target is published.
	Engagement *Engagement
}

// Engagement states (ADR 0018).
const (
	EngagementCollecting  = "collecting"
	EngagementDone        = "done"
	EngagementDeleted     = "deleted"
	EngagementUnsupported = "unsupported"
)

// Engagement is a published target's latest engagement reading (ADR 0018).
type Engagement struct {
	platform.Counts
	State string
	// ReadAt is nil until the first reading.
	ReadAt     *time.Time
	NextReadAt *time.Time
	Error      string
}

// EngagementReading is one reading of a target's engagement.
type EngagementReading struct {
	platform.Counts
	ReadAt time.Time
}

// Attempt is one try at publishing a target.
type Attempt struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	TargetID   uuid.UUID
	Attempt    int
	StartedAt  time.Time
	FinishedAt *time.Time
	Outcome    string
	ErrorCode  string
	Error      string
}

// Event records something that happened (ADR 0012).
type Event struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Livemode  bool
	Type      string
	Data      json.RawMessage
	RequestID string
	CreatedAt time.Time
}

// WebhookEndpoint receives events.
type WebhookEndpoint struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	Livemode       bool
	URL            string
	Description    string
	EventTypes     []string
	Secret         []byte // encrypted
	Status         string
	DisabledReason string
	FailingSince   *time.Time
	CreatedAt      time.Time
}

// Wants reports whether the endpoint subscribes to event type t.
func (w *WebhookEndpoint) Wants(t string) bool {
	for _, e := range w.EventTypes {
		if e == "*" || e == t {
			return true
		}
	}
	return false
}

// Delivery is one event sent (or to be sent) to one endpoint.
type Delivery struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	EndpointID     uuid.UUID
	EventID        uuid.UUID
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	ResponseStatus *int
	ResponseBody   string
	Error          string
	DurationMS     *int
	LastAttemptAt  *time.Time
	CreatedAt      time.Time
	EventType      string // filled by listings
}

// AuditEvent records who did what (ADR 0004).
type AuditEvent struct {
	ID        uuid.UUID
	OrgID     *uuid.UUID
	ActorUser *uuid.UUID
	ActorKey  *uuid.UUID
	Action    string
	Target    string
	Outcome   string
	RequestID string
	Detail    map[string]any
	CreatedAt time.Time
}
