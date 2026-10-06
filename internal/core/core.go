// SPDX-License-Identifier: AGPL-3.0-or-later

// Package core holds Araldo's use cases: signing in, tenancy, channels,
// templates, posts, publishing, events and webhooks. The API, dashboard and
// CLI call it and never the store directly (ADR 0002). Every tenant
// operation takes an Actor and is scoped to the actor's org and mode
// (ADR 0004, 0006).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/netguard"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Config holds settings the use cases need.
type Config struct {
	// BaseURL is the server's public URL (sandbox permalinks, links in
	// events).
	BaseURL string
	// PrivateWebhooks are the non-public addresses webhook deliveries, and
	// media fetched by URL, may reach (for self-hosters, ADR 0012).
	PrivateWebhooks netguard.Policy
	// Blobs, when set, stores new media files; otherwise they go in
	// Postgres (ADR 0017).
	Blobs Blobs
	// AdNetworks are the ad networks live accounts can connect to (ADR
	// 0023); test mode always has the sandbox.
	AdNetworks []ads.Reporter
	// AnalyticsSources are the web analytics tools live sites can connect
	// to (ADR 0025); test mode always has the sandbox.
	AnalyticsSources []analytics.Source
	// Mailers are the email providers live newsletters can be sent
	// through (ADR 0024); test mode always has the sandbox.
	Mailers []email.Mailer
	// MaxVideoBytes is the largest video accepted (ADR 0027); zero is
	// DefaultMaxVideoBytes.
	MaxVideoBytes int64
	// SignupURL, when set, is where people create an account and org; then
	// members cannot create orgs themselves (ADR 0031).
	SignupURL string
	// BillingURL, when set, is where owners are sent, with a hand-off
	// signed with BillingLinkKey, to manage billing (ADR 0031).
	BillingURL     string
	BillingLinkKey []byte
	// Mail, when set, sends the install's own email to its users (ADR
	// 0034); without it, none is sent.
	Mail smtpmail.Sender
}

// Service is the application.
type Service struct {
	store     *store.Store
	keys      *keyring.Keyring
	platforms *platform.Registry
	log       *slog.Logger
	cfg       Config
	// Now is the clock; tests replace it.
	Now func() time.Time
	// HTTP is the client for webhook deliveries.
	HTTP *http.Client
	// MediaHTTP fetches media by URL.
	MediaHTTP *http.Client
	blobs     Blobs
	// adNetworks are the ad networks accounts can be read from.
	adNetworks *ads.Registry
	// analytics are the web analytics tools sources can be read from.
	analytics *analytics.Registry
	// mailers are the email providers newsletters can be sent through.
	mailers *email.Registry
	// metrics records nothing until Instrument.
	metrics *metrics
	// schemaSeen is set once Ready has found the schema current.
	schemaSeen atomic.Bool
	// denied paces the recording of denials.
	denied denials
	// requests buffers the request log on its way to the database (ADR 0032).
	requests requestLog
	// tracer makes spans (ADR 0014); a no-op until Trace.
	tracer trace.Tracer
	// engagementResumed is set once posts on newly readable platforms were
	// scheduled (resumeEngagement).
	engagementResumed atomic.Bool
	// SSOHTTP reaches identity providers (ADR 0033).
	SSOHTTP *http.Client
	// LookupTXT reads DNS TXT records, to verify single sign-on domains.
	LookupTXT func(ctx context.Context, name string) ([]string, error)
}

// New returns the application.
func New(st *store.Store, keys *keyring.Keyring, platforms *platform.Registry, log *slog.Logger, cfg Config) *Service {
	return &Service{store: st, keys: keys, platforms: platforms, log: log, cfg: cfg, Now: time.Now,
		adNetworks: ads.NewRegistry(append([]ads.Reporter{ads.SandboxAds{}}, cfg.AdNetworks...)...),
		analytics:  analytics.NewRegistry(append([]analytics.Source{analytics.SandboxSource{}}, cfg.AnalyticsSources...)...),
		mailers:    email.NewRegistry(append([]email.Mailer{email.SandboxMailer{}}, cfg.Mailers...)...),
		HTTP:       netguard.Client(cfg.PrivateWebhooks, deliveryTimeout),
		MediaHTTP:  mediaClient(netguard.Client(cfg.PrivateWebhooks, mediaFetchTimeout)),
		SSOHTTP:    netguard.Client(cfg.PrivateWebhooks, ssoHTTPTimeout),
		LookupTXT:  net.DefaultResolver.LookupTXT,
		blobs:      cfg.Blobs, metrics: noopMetrics(), tracer: tracenoop.NewTracerProvider().Tracer("")}
}

// Store exposes the store to the composition root (health checks).
func (s *Service) Store() *store.Store { return s.store }

// Platforms is the adapter registry.
func (s *Service) Platforms() *platform.Registry { return s.platforms }

// Permission names an action; API key scopes use the same names.
type Permission string

// Permissions.
const (
	PermPostsRead      Permission = "posts:read"
	PermPostsWrite     Permission = "posts:write"
	PermPostsApprove   Permission = "posts:approve"
	PermTemplatesRead  Permission = "templates:read"
	PermTemplatesWrite Permission = "templates:write"
	PermChannelsRead   Permission = "channels:read"
	PermChannelsWrite  Permission = "channels:write"
	PermBrandsRead     Permission = "brands:read"
	PermBrandsWrite    Permission = "brands:write"
	PermEventsRead     Permission = "events:read"
	PermWebhooksRead   Permission = "webhooks:read"
	PermWebhooksWrite  Permission = "webhooks:write"
	PermKeysWrite      Permission = "keys:write"
	PermMembersWrite   Permission = "members:write"
	PermOrgWrite       Permission = "org:write"
	PermAuditRead      Permission = "audit:read"
	PermAdsRead        Permission = "ads:read"
	PermAdsWrite       Permission = "ads:write"
	// Newsletters (ADR 0024); approving one takes posts:approve.
	PermNewslettersRead  Permission = "newsletters:read"
	PermNewslettersWrite Permission = "newsletters:write"
)

// KeyScopes are the integration permissions: an API key with no scopes
// listed holds them all, or it holds those it lists.
var KeyScopes = []Permission{
	PermPostsRead, PermPostsWrite, PermTemplatesRead, PermTemplatesWrite, PermChannelsRead, PermChannelsWrite,
	PermBrandsRead, PermBrandsWrite, PermEventsRead, PermWebhooksRead, PermWebhooksWrite, PermAdsRead,
	PermNewslettersRead, PermNewslettersWrite,
}

// AdminScopes are explicit-only (ADR 0019): a key holds one only when it
// lists it by name, never through "no scopes, full access", and only a
// member can create a key holding one.
var AdminScopes = []Permission{PermKeysWrite, PermPostsApprove, PermAuditRead, PermAdsWrite}

var roleMin = map[Permission]model.Role{
	PermPostsRead: model.RoleViewer, PermTemplatesRead: model.RoleViewer, PermChannelsRead: model.RoleViewer,
	PermBrandsRead: model.RoleViewer, PermEventsRead: model.RoleViewer, PermWebhooksRead: model.RoleViewer, PermAdsRead: model.RoleViewer,
	PermNewslettersRead: model.RoleViewer,
	PermPostsWrite:      model.RoleEditor, PermTemplatesWrite: model.RoleEditor, PermNewslettersWrite: model.RoleEditor,
	PermChannelsWrite: model.RoleAdmin, PermBrandsWrite: model.RoleAdmin, PermWebhooksWrite: model.RoleAdmin,
	PermKeysWrite: model.RoleAdmin, PermMembersWrite: model.RoleAdmin, PermPostsApprove: model.RoleAdmin, PermAuditRead: model.RoleAdmin,
	PermAdsWrite: model.RoleAdmin,
	PermOrgWrite: model.RoleOwner,
}

// Actor is who is calling: a member through the dashboard, or an API key.
type Actor struct {
	OrgID    uuid.UUID
	Livemode bool
	// Member (dashboard, or a user token).
	UserID *uuid.UUID
	Role   model.Role
	// TokenID is the user token a member acts through, from the CLI.
	TokenID *uuid.UUID
	// API key.
	KeyID   *uuid.UUID
	Scopes  []string // empty: full access
	BrandID *uuid.UUID
	// KeyExpiresAt is when the key stops working; nil if never. The keys it
	// creates or rolls never outlive it.
	KeyExpiresAt *time.Time
	// RequestID ties events and audit entries to a request.
	RequestID string
	// Operator marks the server's operator acting through `araldo admin`
	// (ADR 0028), who already holds the database and master keys: an
	// owner's permissions, no member, and no sudo mode, which would add
	// nothing. OperatorCommand is the command, for the audit log.
	Operator        bool
	OperatorCommand string
	// OperatorKeyID is the operator key acting through the operator API
	// (ADR 0031), for the audit log.
	OperatorKeyID *uuid.UUID
	// OrgStatus is the org's status when the actor was made (ADR 0031):
	// read-only refuses changes, suspended everything.
	OrgStatus model.OrgStatus
}

// IsKey reports whether the actor is an API key.
func (a Actor) IsKey() bool { return a.KeyID != nil }

// Can reports whether the actor has permission p.
func (a Actor) Can(p Permission) bool {
	if a.IsKey() {
		switch {
		case p == PermMembersWrite, p == PermOrgWrite:
			return false // people only
		case slices.Contains(AdminScopes, p):
			return slices.Contains(a.Scopes, string(p)) // explicit only
		}
		return len(a.Scopes) == 0 || slices.Contains(a.Scopes, string(p))
	}
	minRole, ok := roleMin[p]
	return ok && a.Role.AtLeast(minRole)
}

func (a Actor) require(p Permission) error {
	if err := a.statusAllows(p); err != nil {
		return err
	}
	return a.permitted(p)
}

// requireToRead is require for reading what p manages (an org's API keys
// need keys:write to see): a read-only org still reads it.
func (a Actor) requireToRead(p Permission) error {
	if !a.Operator && a.OrgStatus == model.OrgSuspended {
		return errOrgSuspended
	}
	return a.permitted(p)
}

func (a Actor) permitted(p Permission) error {
	if !a.Can(p) {
		if a.IsKey() {
			return &apperr.Error{Kind: apperr.KindForbidden, Code: "scope_missing", Message: "This API key lacks the " + string(p) + " scope."}
		}
		return apperr.Forbidden("Your role (%s) cannot do this.", a.Role)
	}
	return nil
}

// statusAllows checks the org's status lets the actor use p: a suspended
// org nothing, a read-only one only reading (ADR 0031). The operator is
// never held back.
func (a Actor) statusAllows(p Permission) error {
	switch {
	case a.Operator:
	case a.OrgStatus == model.OrgSuspended:
		return errOrgSuspended
	case a.OrgStatus == model.OrgReadOnly && !strings.HasSuffix(string(p), ":read"):
		return &apperr.Error{Kind: apperr.KindForbidden, Code: "org_read_only",
			Message: "This org is read-only: it can read, but not change anything. An owner can find out why under Org settings."}
	}
	return nil
}

var errOrgSuspended = &apperr.Error{Kind: apperr.KindForbidden, Code: "org_suspended",
	Message: "This org is suspended. An owner can find out why by signing in to the dashboard."}

// orgWide refuses a key limited to one brand what spans the whole org:
// events and webhook endpoints carry every brand's activity.
func (a Actor) orgWide(what string) error {
	if a.BrandID != nil {
		return apperr.Forbidden("This API key is limited to one brand, and %s cover the whole org: use a key for every brand.", what)
	}
	return nil
}

// brandAllowed checks a key restricted to one brand.
func (a Actor) brandAllowed(brandID uuid.UUID) error {
	if a.BrandID != nil && *a.BrandID != brandID {
		return apperr.NotFound("brand")
	}
	return nil
}

// notFound maps a store miss to a 404 naming what was missing.
func notFound(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return apperr.NotFound(what)
	}
	return err
}

// emit records an event in the current transaction (ADR 0012). Its type
// must be in EventTypes, the list endpoints subscribe from: one emitted and
// not listed could never be subscribed to, so it fails here, in the first
// test that reaches it, rather than going quietly undelivered.
// (TestEventTypesMatchContract keeps the list and the contract in step.)
func (s *Service) emit(ctx context.Context, tx *store.Store, orgID uuid.UUID, livemode bool, requestID, typ string, object any) error {
	if !slices.Contains(EventTypes, typ) {
		return fmt.Errorf("core: emitting event %q, which is not in core.EventTypes: add it there and to the EventType enum in api/openapi.yaml", typ)
	}
	data, err := json.Marshal(map[string]any{"object": object})
	if err != nil {
		return err
	}
	return tx.CreateEvent(ctx, &model.Event{ID: id.New(), OrgID: orgID, Livemode: livemode, Type: typ, Data: data, RequestID: requestID})
}

// audit records who did what, in the current transaction.
func (s *Service) audit(ctx context.Context, tx *store.Store, a Actor, action, target string, detail map[string]any) error {
	org := a.OrgID
	var orgp *uuid.UUID
	if org != uuid.Nil {
		orgp = &org
	}
	e := &model.AuditEvent{ID: id.New(), OrgID: orgp, ActorUser: a.UserID, ActorKey: a.KeyID, Action: action, Target: target,
		RequestID: a.RequestID, Detail: detail}
	if a.TokenID != nil {
		// Through the CLI: which token, so its changes are told apart.
		e.Detail = map[string]any{"user_token": id.Format(id.UserToken, *a.TokenID)}
		for k, v := range detail {
			e.Detail[k] = v
		}
	}
	if a.Operator {
		// The operator, not a member: what they ran is the record.
		e.ActorUser, e.ActorKey = nil, nil
		e.Detail = map[string]any{"operator_command": a.OperatorCommand}
		if a.OperatorKeyID != nil {
			e.Detail = map[string]any{"operator_key": id.Format(id.OperatorKey, *a.OperatorKeyID)}
		}
		for k, v := range detail {
			e.Detail[k] = v
		}
	}
	return tx.RecordAudit(ctx, e)
}

func ptr[T any](v T) *T { return &v }
