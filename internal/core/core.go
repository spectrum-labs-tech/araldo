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
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/netguard"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Config holds settings the use cases need.
type Config struct {
	// BaseURL is the server's public URL (sandbox permalinks, links in
	// events).
	BaseURL string
	// AllowPrivateWebhooks lets webhook deliveries, and media fetched by
	// URL, reach private networks (for self-hosters, ADR 0012).
	AllowPrivateWebhooks bool
	// Blobs, when set, stores new media files; otherwise they go in
	// Postgres (ADR 0017).
	Blobs Blobs
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
	// metrics records nothing until Instrument.
	metrics *metrics
}

// New returns the application.
func New(st *store.Store, keys *keyring.Keyring, platforms *platform.Registry, log *slog.Logger, cfg Config) *Service {
	return &Service{store: st, keys: keys, platforms: platforms, log: log, cfg: cfg, Now: time.Now,
		HTTP:      netguard.Client(cfg.AllowPrivateWebhooks, deliveryTimeout),
		MediaHTTP: mediaClient(cfg.AllowPrivateWebhooks, netguard.Client(cfg.AllowPrivateWebhooks, mediaFetchTimeout)),
		blobs:     cfg.Blobs, metrics: noopMetrics()}
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
)

// KeyScopes are the permissions an API key may be restricted to.
var KeyScopes = []Permission{
	PermPostsRead, PermPostsWrite, PermTemplatesRead, PermTemplatesWrite, PermChannelsRead, PermChannelsWrite,
	PermBrandsRead, PermBrandsWrite, PermEventsRead, PermWebhooksRead, PermWebhooksWrite,
}

var roleMin = map[Permission]model.Role{
	PermPostsRead: model.RoleViewer, PermTemplatesRead: model.RoleViewer, PermChannelsRead: model.RoleViewer,
	PermBrandsRead: model.RoleViewer, PermEventsRead: model.RoleViewer, PermWebhooksRead: model.RoleViewer,
	PermPostsWrite: model.RoleEditor, PermTemplatesWrite: model.RoleEditor,
	PermChannelsWrite: model.RoleAdmin, PermBrandsWrite: model.RoleAdmin, PermWebhooksWrite: model.RoleAdmin,
	PermKeysWrite: model.RoleAdmin, PermMembersWrite: model.RoleAdmin, PermPostsApprove: model.RoleAdmin,
	PermOrgWrite: model.RoleOwner,
}

// Actor is who is calling: a member through the dashboard, or an API key.
type Actor struct {
	OrgID    uuid.UUID
	Livemode bool
	// Member (dashboard).
	UserID *uuid.UUID
	Role   model.Role
	// API key.
	KeyID   *uuid.UUID
	Scopes  []string // empty: full access
	BrandID *uuid.UUID
	// RequestID ties events and audit entries to a request.
	RequestID string
}

// IsKey reports whether the actor is an API key.
func (a Actor) IsKey() bool { return a.KeyID != nil }

// Can reports whether the actor has permission p.
func (a Actor) Can(p Permission) bool {
	if a.IsKey() {
		switch p {
		case PermKeysWrite, PermMembersWrite, PermOrgWrite, PermPostsApprove:
			return false // people only
		}
		return len(a.Scopes) == 0 || slices.Contains(a.Scopes, string(p))
	}
	minRole, ok := roleMin[p]
	return ok && a.Role.AtLeast(minRole)
}

func (a Actor) require(p Permission) error {
	if !a.Can(p) {
		if a.IsKey() {
			return &apperr.Error{Kind: apperr.KindForbidden, Code: "scope_missing", Message: "This API key lacks the " + string(p) + " scope."}
		}
		return apperr.Forbidden("Your role (%s) cannot do this.", a.Role)
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

// emit records an event in the current transaction (ADR 0012).
func (s *Service) emit(ctx context.Context, tx *store.Store, orgID uuid.UUID, livemode bool, requestID, typ string, object any) error {
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
	return tx.RecordAudit(ctx, &model.AuditEvent{
		ID: id.New(), OrgID: orgp, ActorUser: a.UserID, ActorKey: a.KeyID, Action: action, Target: target,
		RequestID: a.RequestID, Detail: detail,
	})
}

func ptr[T any](v T) *T { return &v }
