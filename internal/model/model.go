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
	// HasPasskey is set when the user has a passkey (ADR 0007).
	HasPasskey   bool
	FailedLogins int
	LockedUntil  *time.Time
	CreatedAt    time.Time
}

// MFAEnabled reports whether the user has a second factor.
func (u *User) MFAEnabled() bool { return u.TOTPEnabledAt != nil }

// SecondFactor reports whether the user has a second factor: TOTP, or a
// passkey, which also signs in alone (ADR 0007). A password sign-in then
// needs it, and it meets an org's two-factor requirement.
func (u *User) SecondFactor() bool { return u.MFAEnabled() || u.HasPasskey }

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
	// Status, its note, Limits and ExternalRef are set by the operator
	// (ADR 0031). ExternalRef is "" when unset.
	Status      OrgStatus
	StatusNote  string
	Limits      OrgLimits
	ExternalRef string
	CreatedAt   time.Time
}

// OrgStatus is what an org may do (ADR 0031).
type OrgStatus string

const (
	// OrgActive: everything.
	OrgActive OrgStatus = "active"
	// OrgReadOnly: members and keys read but change nothing; scheduled
	// posts still go out.
	OrgReadOnly OrgStatus = "read_only"
	// OrgSuspended: keys are refused, members see a notice, and nothing is
	// published or sent.
	OrgSuspended OrgStatus = "suspended"
)

// Valid reports whether s is a known status.
func (s OrgStatus) Valid() bool { return s == OrgActive || s == OrgReadOnly || s == OrgSuspended }

// OrgLimits caps what an org may have (ADR 0031); nil is unlimited.
type OrgLimits struct {
	Brands     *int `json:"brands"`
	Channels   *int `json:"channels"`
	Members    *int `json:"members"`
	PostsMonth *int `json:"posts_per_month"`
}

// OrgUsage is what an org has and did in a month (ADR 0031).
type OrgUsage struct {
	Brands       int
	Channels     int // live
	Members      int // with open invitations
	PostsCreated int // live, in the month
	TargetsSent  int // live, published in the month
	MediaBytes   int64
	PeriodStart  time.Time
	PeriodEnd    time.Time
}

// OperatorKey authenticates the operator API (ADR 0031).
type OperatorKey struct {
	ID         uuid.UUID
	Name       string
	Hint       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
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

// Strictness orders policies by how many posts they hold for review.
func (p ApprovalPolicy) Strictness() int {
	switch p {
	case ApprovalEditorsAndKey:
		return 1
	case ApprovalAll:
		return 2
	}
	return 0
}

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
	// EmailTheme is its look in newsletters (ADR 0024).
	EmailTheme EmailTheme
	CreatedAt  time.Time
	// Slots are its weekly publishing times, filled by reads.
	Slots []Slot
}

// EmailTheme is a brand's look in newsletters (ADR 0024 decision 9).
type EmailTheme struct {
	LogoMediaID *uuid.UUID
	// Accent is "#rrggbb", or empty for the default.
	Accent string
	// PostalAddress is the sender's, which anti-spam laws require in every
	// newsletter; an issue cannot be scheduled without it.
	PostalAddress string
	Footer        string
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
	// TokenExpiresAt when its token expires (ADR 0021). InstallApp says the
	// app is the install's, not the org's (ADR 0030).
	AppID          *uuid.UUID
	InstallApp     bool
	TokenExpiresAt *time.Time
	// CheckedAt is the last daily health check, and CheckError what it
	// found wrong ("" when it passed).
	CheckedAt  *time.Time
	CheckError string
}

// ProviderApp is a developer app on a platform: an org's (ADR 0021), or,
// with Install, one the install provides to every org (ADR 0030; OrgID is
// then zero).
type ProviderApp struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	Install  bool
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
	OrgID   uuid.UUID
	UserID  uuid.UUID
	BrandID uuid.UUID
	AppID   uuid.UUID
	// InstallApp says AppID is an install app (ADR 0030).
	InstallApp bool
	Provider   platform.Provider
	Verifier   string
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
	// PublishAt is nil while a next_slot post waits for approval; it takes
	// its slot when approved (ADR 0022). PublishBy is nil with it, unless
	// the caller gave one.
	PublishAt      *time.Time
	PublishBy      *time.Time
	SlotAt         *time.Time
	Metadata       map[string]string
	ApprovalNeeded bool
	ReviewedBy     *uuid.UUID
	ReviewedByKey  *uuid.UUID
	ReviewedAt     *time.Time
	ReviewNote     string
	CreatedByUser  *uuid.UUID
	CreatedByKey   *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Targets        []Target
	// Media are attached in this order (ADR 0017).
	Media []*Media
}

// Slotted reports whether the post holds one of its brand's slots, or
// takes one when approved.
func (p *Post) Slotted() bool { return p.SlotAt != nil || p.PublishAt == nil }

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
	// Transparent is set for an image with transparent pixels, which is
	// never resized into a JPEG (ADR 0027).
	Transparent bool
	// A video's length, frames per second and codecs, from its index (ADR
	// 0027); zero and empty for an image.
	DurationMS int64
	FrameRate  float64
	VideoCodec string
	AudioCodec string
	SHA256     []byte
	Alt        string
	Filename   string
	// Storage is where the file is: StoragePostgres, or StorageS3 at
	// StorageKey.
	Storage       string
	StorageKey    string
	CreatedByUser *uuid.UUID
	CreatedByKey  *uuid.UUID
	CreatedAt     time.Time
}

// AdAccountStatus is whether an ad account can be read (ADR 0023).
type AdAccountStatus string

// Ad account statuses.
const (
	AdAccountActive      AdAccountStatus = "active"
	AdAccountNeedsReauth AdAccountStatus = "needs_reauth"
)

// AdAccount is an ad network account connected to a brand, read for its
// campaigns' results (ADR 0023).
type AdAccount struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	BrandID    uuid.UUID
	Livemode   bool
	Network    string
	ExternalID string
	Name       string
	// Currency is the ISO 4217 code its amounts are in, and Timezone the
	// zone its days are in, both as the network reports them.
	Currency    string
	Timezone    string
	Settings    map[string]string // non-secret fields
	Credentials []byte            // encrypted secret fields
	// AppID is the developer app a signed-in account connected through;
	// InstallApp says it is the install's (ADR 0030).
	AppID      *uuid.UUID
	InstallApp bool
	Status     AdAccountStatus
	StatusNote string
	ReadAt     *time.Time
	NextReadAt time.Time
	CreatedAt  time.Time
}

// AnalyticsSource is a brand's web analytics site, read for visits and
// conversions by UTM tags (ADR 0025).
type AnalyticsSource struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	BrandID  uuid.UUID
	Livemode bool
	Provider string
	// Site is the site or property as the provider names it.
	Site     string
	Name     string
	Timezone string
	// Goals are the provider's goals that count as conversions.
	Goals       []string
	Settings    map[string]string // non-secret fields
	Credentials []byte            // encrypted secret fields
	Status      AdAccountStatus
	StatusNote  string
	ReadAt      *time.Time
	NextReadAt  time.Time
	CreatedAt   time.Time
}

// MailAccount is an email provider account a brand sends newsletters
// through (ADR 0024). The provider owns the subscribers.
type MailAccount struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	BrandID    uuid.UUID
	Livemode   bool
	Provider   string
	ExternalID string
	Name       string
	FromName   string
	FromEmail  string
	ReplyTo    string
	// DefaultAudiences are the provider's audience IDs a new issue goes to.
	DefaultAudiences []string
	Settings         map[string]string // non-secret fields
	Credentials      []byte            // encrypted secret fields
	Status           AdAccountStatus
	StatusNote       string
	CreatedAt        time.Time
}

// IssueStatus is where a newsletter issue stands, derived from its
// deliveries once it is scheduled (ADR 0024 decision 6).
type IssueStatus string

// Issue statuses.
const (
	IssueDraft           IssueStatus = "draft"
	IssuePendingApproval IssueStatus = "pending_approval"
	IssueScheduled       IssueStatus = "scheduled"
	IssueSending         IssueStatus = "sending"
	IssueSent            IssueStatus = "sent"
	IssuePartiallySent   IssueStatus = "partially_sent"
	IssueCanceled        IssueStatus = "canceled"
	IssueFailed          IssueStatus = "failed"
)

// Issue is a newsletter issue: a document sent to audiences at one or
// more mail accounts.
type Issue struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	BrandID        uuid.UUID
	Livemode       bool
	Subject        string
	PreviewText    string
	Body           string
	Status         IssueStatus
	SendAt         *time.Time
	ApprovalNeeded bool
	ReviewedByUser *uuid.UUID
	ReviewedByKey  *uuid.UUID
	ReviewedAt     *time.Time
	ReviewNote     string
	CreatedByUser  *uuid.UUID
	CreatedByKey   *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Deliveries and Media are filled by reads.
	Deliveries []IssueDelivery
	Media      []uuid.UUID
}

// DeliveryStatus is where one mail account's copy of an issue stands.
type DeliveryStatus string

// Delivery statuses.
const (
	DeliveryDraft DeliveryStatus = "draft"
	// DeliveryHeld waits for the issue's approval.
	DeliveryHeld DeliveryStatus = "held"
	// DeliveryQueued waits to be handed to the provider.
	DeliveryQueued DeliveryStatus = "queued"
	// DeliveryHandedOff is scheduled at the provider, which sends it.
	DeliveryHandedOff      DeliveryStatus = "handed_off"
	DeliverySent           DeliveryStatus = "sent"
	DeliveryCanceled       DeliveryStatus = "canceled"
	DeliveryFailed         DeliveryStatus = "failed"
	DeliveryNeedsAttention DeliveryStatus = "needs_attention"
)

// Open reports whether a delivery may still be sent.
func (s DeliveryStatus) Open() bool {
	switch s {
	case DeliveryDraft, DeliveryHeld, DeliveryQueued, DeliveryHandedOff:
		return true
	}
	return false
}

// Audience is a provider's audience an issue goes to, as it was when
// chosen: never its members.
type Audience struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

// MailResults are a delivery's counts, as its provider reports them.
type MailResults struct {
	Recipients   int64
	Delivered    int64
	Opens        int64
	Clicks       int64
	Unsubscribes int64
	Bounces      int64
	Complaints   int64
}

// IssueDelivery is one mail account's copy of an issue: its audiences, its
// campaign at the provider and its results.
type IssueDelivery struct {
	ID      uuid.UUID
	OrgID   uuid.UUID
	IssueID uuid.UUID
	// MailAccountID is nil once the account is disconnected.
	MailAccountID *uuid.UUID
	Livemode      bool
	Provider      string
	AccountName   string
	Audiences     []Audience
	Status        DeliveryStatus
	HandoffAt     *time.Time
	Attempts      int
	CampaignID    string
	LastError     string
	SentAt        *time.Time
	Results       MailResults
	ResultsReadAt *time.Time
	NextReadAt    *time.Time
	UpdatedAt     time.Time
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
	ID        uuid.UUID
	OrgID     uuid.UUID
	PostID    uuid.UUID
	ChannelID uuid.UUID
	Livemode  bool
	Provider  platform.Provider
	Parts     []string
	Status    TargetStatus
	Attempts  int
	// NextAttemptAt and PublishBy are nil on a held target whose post
	// waits for a slot (ADR 0022).
	NextAttemptAt *time.Time
	PublishBy     *time.Time
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

// Invitation asks a person to join an org with a role (ADR 0004). Its
// token is shown once, in the link the inviter shares.
type Invitation struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	OrgName    string
	Email      string
	Role       Role
	InvitedBy  *uuid.UUID
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	CreatedAt  time.Time
}

// UserToken is a person's own credential for the CLI (ADR 0028), in one
// mode, for whichever of their orgs a request names.
type UserToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Livemode   bool
	Name       string // the device it was issued to
	Hint       string
	CreatedIP  string
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// DeviceAuthorization is one device sign-in (RFC 8628): a CLI waiting for
// a person to approve its user code in the dashboard.
type DeviceAuthorization struct {
	ID           uuid.UUID
	UserCode     string
	DeviceName   string
	Livemode     bool
	ClientIP     string
	Status       string // pending, approved, denied or issued
	UserID       *uuid.UUID
	PollInterval int // seconds
	LastPolledAt *time.Time
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// WebhookEndpoint receives events.
type WebhookEndpoint struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	Livemode    bool
	URL         string
	Description string
	EventTypes  []string
	Secret      []byte // encrypted
	// PreviousSecret (encrypted) signs deliveries beside Secret until
	// PreviousSecretUntil, after the secret is rolled.
	PreviousSecret      []byte
	PreviousSecretUntil *time.Time
	Status              string
	DisabledReason      string
	FailingSince        *time.Time
	CreatedAt           time.Time
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

// APIRequest is one authenticated API request, for the org's request log
// (ADR 0032). It holds no body or query string.
type APIRequest struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	Livemode    bool
	Method      string
	Route       string // the route's pattern, such as POST /v1/posts/{id}/cancel
	Path        string
	Status      int
	ErrorCode   string
	DurationMS  int
	KeyID       *uuid.UUID
	UserTokenID *uuid.UUID
	RequestID   string
	CreatedAt   time.Time
}

// ReportShare is a link to a brand's report for a month, for people
// outside the org (ADR 0026).
type ReportShare struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	BrandID   uuid.UUID
	Livemode  bool
	Month     string // YYYY-MM
	CreatedBy *uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// Passkey is a user's WebAuthn credential (ADR 0007).
type Passkey struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}
