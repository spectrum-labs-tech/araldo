// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/opsched"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// ResolveBrand finds a brand by ID ("brand_…") or slug.
func (s *Service) ResolveBrand(ctx context.Context, a Actor, ref string) (*model.Brand, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, apperr.Invalid("brand_required", "brand", "Name a brand by ID or slug.")
	}
	if strings.HasPrefix(ref, string(id.Brand)+"_") {
		bid, err := ParseID(id.Brand, ref, "brand")
		if err != nil {
			return nil, err
		}
		return s.Brand(ctx, a, bid)
	}
	b, err := s.store.BrandBySlug(ctx, a.OrgID, ref)
	if err != nil {
		return nil, notFoundID("brand", ref)
	}
	if err := a.brandAllowed(b.ID); err != nil {
		return nil, notFoundID("brand", ref)
	}
	return b, nil
}

// Idempotency (ADR 0005).

// IdempotencyWindow is how long a key is remembered.
const IdempotencyWindow = 24 * time.Hour

// idempotencyAbandoned is how long a key may stay in progress before a
// retry of the same request takes it over: far longer than any request
// runs (the server's timeouts are a minute), so its process is gone.
const idempotencyAbandoned = 5 * time.Minute

// IdempotentReplay is a stored response to replay.
type IdempotentReplay struct {
	Status int
	Body   []byte
}

// BeginIdempotent claims an idempotency key for a request. It returns a
// replay when the key already completed with the same request, and a 409
// when it is in use or was used for a different request.
func (s *Service) BeginIdempotent(ctx context.Context, a Actor, key string, fingerprint []byte) (*IdempotentReplay, error) {
	if a.KeyID == nil {
		return nil, nil
	}
	if len(key) > 255 {
		return nil, apperr.Invalid("idempotency_key_invalid", "Idempotency-Key", "Idempotency keys are at most 255 characters.")
	}
	now := s.Now()
	rec, claimed, err := s.store.BeginIdempotent(ctx, *a.KeyID, key, fingerprint, now.Add(-IdempotencyWindow), now.Add(-idempotencyAbandoned))
	if err != nil || claimed {
		return nil, err
	}
	if string(rec.Fingerprint) != string(fingerprint) {
		return nil, apperr.Conflict("idempotency_key_reused", "This Idempotency-Key was used for a different request.")
	}
	if rec.Status != "done" {
		return nil, apperr.Conflict("idempotency_key_in_use", "A request with this Idempotency-Key is still in progress; retry shortly.")
	}
	return &IdempotentReplay{Status: rec.ResponseStatus, Body: rec.ResponseBody}, nil
}

// FinishIdempotent stores the response for a claimed key. Only a
// request that took effect (2xx) is kept: an error had no side effects, so
// releasing the key lets a corrected retry run (ADR 0019, as Stripe does).
func (s *Service) FinishIdempotent(ctx context.Context, a Actor, key string, status int, body []byte) error {
	if a.KeyID == nil {
		return nil
	}
	if status < 200 || status > 299 {
		status = 0
	}
	return s.store.FinishIdempotent(context.WithoutCancel(ctx), *a.KeyID, key, status, body)
}

// Housekeeping tasks (opsched).

// Tasks returns the periodic tasks a worker runs.
func (s *Service) Tasks() []opsched.Task {
	return []opsched.Task{
		{Name: "publish.reclaim", Interval: 30 * time.Second, Run: s.ReclaimLostTargets},
		{Name: "publish.expire", Interval: time.Minute, Run: s.ExpireOverdue},
		{Name: "webhooks.reclaim", Interval: 30 * time.Second, Run: func(ctx context.Context) (int, error) {
			return s.store.ReclaimDeliveries(ctx, s.Now())
		}},
		{Name: "webhooks.disable_failing", Interval: 10 * time.Minute, Run: s.DisableFailingEndpoints},
		{Name: "events.prune", Interval: time.Hour, Run: func(ctx context.Context) (int, error) {
			return s.store.PruneEvents(ctx, s.Now().Add(-30*24*time.Hour))
		}},
		{Name: "idempotency.prune", Interval: time.Hour, Run: func(ctx context.Context) (int, error) {
			return s.store.PruneIdempotencyKeys(ctx, s.Now().Add(-IdempotencyWindow))
		}},
		{Name: "sessions.prune", Interval: time.Hour, Run: func(ctx context.Context) (int, error) {
			return s.store.PruneSessions(ctx, s.Now())
		}},
		{Name: "media.prune", Interval: time.Hour, Run: s.PruneUnusedMedia},
		{Name: "channels.refresh", Interval: time.Hour, Timeout: 10 * time.Minute, Run: s.RefreshTokens},
		{Name: "channels.check", Interval: time.Hour, Timeout: 10 * time.Minute, Run: s.CheckChannels},
		{Name: "oauth.prune", Interval: time.Hour, Run: func(ctx context.Context) (int, error) {
			return s.store.PruneOAuthStates(ctx, s.Now().Add(-OAuthStateTTL))
		}},
		{Name: "engagement.collect", Interval: 2 * time.Minute, Timeout: 5 * time.Minute, Run: s.CollectEngagement},
		{Name: "ads.collect", Interval: 10 * time.Minute, Timeout: 5 * time.Minute, Run: s.CollectAds},
		{Name: "analytics.collect", Interval: 10 * time.Minute, Timeout: 5 * time.Minute, Run: s.CollectAnalytics},
		{Name: "newsletters.handoff", Interval: time.Minute, Timeout: 5 * time.Minute, Run: s.HandOffNewsletters},
		{Name: "newsletters.read", Interval: 5 * time.Minute, Timeout: 5 * time.Minute, Run: s.ReadNewsletterResults},
	}
}

// TaskStatuses lists background tasks for operators.
func (s *Service) TaskStatuses(ctx context.Context, a Actor) ([]opsched.TaskStatus, error) {
	if !a.Can(PermOrgWrite) {
		return nil, apperr.Forbidden("Only owners can see background tasks.")
	}
	return s.store.Tasks(ctx)
}

// QueueStats summarizes the actor's publishing queue (its org and mode).
func (s *Service) QueueStats(ctx context.Context, a Actor) (store.QueueStats, error) {
	if err := a.require(PermPostsRead); err != nil {
		return store.QueueStats{}, err
	}
	return s.store.QueueStats(ctx, a.OrgID, a.Livemode, s.Now())
}

// AuditEvents lists the org's audit trail.
func (s *Service) AuditEvents(ctx context.Context, a Actor, page store.Page) ([]model.AuditEvent, bool, error) {
	if err := a.require(PermAuditRead); err != nil {
		return nil, false, err
	}
	return s.store.AuditEvents(ctx, a.OrgID, page)
}

// Attempts lists a target's publish attempts.
func (s *Service) Attempts(ctx context.Context, a Actor, targetID uuid.UUID) ([]model.Attempt, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	t, err := s.store.Target(ctx, a.OrgID, targetID)
	if err != nil || t.Livemode != a.Livemode {
		return nil, apperr.NotFound("post target")
	}
	if _, err := s.Post(ctx, a, t.PostID); err != nil {
		return nil, err // a key limited to another brand
	}
	return s.store.Attempts(ctx, a.OrgID, targetID)
}

// Ready reports whether the database is reachable and the schema current.
func (s *Service) Ready(ctx context.Context) error {
	v, dirty, err := s.store.SchemaState(ctx)
	if err != nil {
		return err
	}
	latest, err := store.LatestVersion()
	if err != nil {
		return err
	}
	return schemaReady(v, dirty, latest)
}

// schemaReady reports whether a binary whose newest migration is latest
// can serve on schema version v. A newer schema is fine, even one whose
// latest migration failed: migrations work with the release before them
// (ADR 0029), and this one keeps serving while the deploy is retried.
func schemaReady(v uint, dirty bool, latest uint) error {
	if v < latest || v == latest && dirty {
		return errors.New("database schema is not up to date")
	}
	return nil
}
