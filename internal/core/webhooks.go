// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Webhooks (ADR 0012).
const (
	deliveryTimeout  = 10 * time.Second
	deliveryLease    = time.Minute
	deliveryBatch    = 20
	deliveryWindow   = 72 * time.Hour
	responseBodyKeep = 1024
	signatureHeader  = "Araldo-Signature"
)

// EventTypes lists every event Araldo emits.
var EventTypes = []string{
	"post.created", "post.approval_requested", "post.approved", "post.rejected", "post.published", "post.partially_published",
	"post.failed", "post.canceled", "post_target.published", "post_target.failed", "post_target.needs_attention",
	"channel.connected", "channel.needs_reauth", "template.version_created", "post.rescheduled",
	"newsletter.created", "newsletter.updated", "newsletter.approval_requested", "newsletter.approved", "newsletter.rejected",
	"newsletter.scheduled", "newsletter.rescheduled", "newsletter.unscheduled", "newsletter.canceled", "newsletter.sent", "newsletter.failed",
	"webhook_endpoint.disabled",
}

func secretAAD(endpointID uuid.UUID) string {
	return keyring.AAD("webhook_endpoints", "secret", endpointID)
}

// EndpointInput creates or changes an endpoint.
type EndpointInput struct {
	URL         string
	Description string
	EventTypes  []string
	Enabled     *bool
}

func (in *EndpointInput) check(livemode bool) error {
	var ps apperr.Problems
	u, err := url.Parse(strings.TrimSpace(in.URL))
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		ps.Add("url_invalid", "url", "The URL must be an absolute http(s) URL.")
	case livemode && u.Scheme != "https":
		ps.Add("url_insecure", "url", "Live mode endpoints must use HTTPS.")
	case u.User != nil:
		ps.Add("url_invalid", "url", "The URL must not contain credentials; Araldo signs every request instead.")
	}
	if len(in.EventTypes) == 0 {
		in.EventTypes = []string{"*"}
	}
	for _, t := range in.EventTypes {
		if t != "*" && !slices.Contains(EventTypes, t) {
			ps.Add("event_type_invalid", "enabled_events", "Unknown event type %q: the types are the EventType enum in /v1/openapi.yaml, or \"*\" for all.", t)
		}
	}
	if len(in.Description) > 500 {
		ps.Add("description_invalid", "description", "Descriptions are at most 500 characters.")
	}
	return ps.Err("The webhook endpoint is not valid.")
}

func newWebhookSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b)
}

// CreateEndpoint adds a webhook endpoint and returns its signing secret,
// shown only now.
func (s *Service) CreateEndpoint(ctx context.Context, a Actor, in EndpointInput) (*model.WebhookEndpoint, string, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return nil, "", err
	}
	if err := a.require(PermWebhooksWrite); err != nil {
		return nil, "", err
	}
	if err := in.check(a.Livemode); err != nil {
		return nil, "", err
	}
	w := &model.WebhookEndpoint{ID: id.New(), OrgID: a.OrgID, Livemode: a.Livemode, URL: strings.TrimSpace(in.URL),
		Description: strings.TrimSpace(in.Description), EventTypes: in.EventTypes, Status: "enabled", CreatedAt: s.Now()}
	secret := newWebhookSecret()
	sealed, err := s.keys.Encrypt(ctx, a.OrgID, secretAAD(w.ID), []byte(secret))
	if err != nil {
		return nil, "", err
	}
	w.Secret = sealed
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateEndpoint(ctx, w); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "webhook_endpoint.create", id.Format(id.WebhookEndpoint, w.ID), map[string]any{"url": w.URL})
	})
	return w, secret, err
}

// Endpoint returns one of the actor's endpoints.
func (s *Service) Endpoint(ctx context.Context, a Actor, endpointID uuid.UUID) (*model.WebhookEndpoint, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return nil, err
	}
	if err := a.require(PermWebhooksRead); err != nil {
		return nil, err
	}
	w, err := s.store.Endpoint(ctx, a.OrgID, endpointID)
	if err != nil || w.Livemode != a.Livemode {
		return nil, apperr.NotFound("webhook endpoint")
	}
	return w, nil
}

// Endpoints lists endpoints in the actor's mode.
func (s *Service) Endpoints(ctx context.Context, a Actor) ([]*model.WebhookEndpoint, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return nil, err
	}
	if err := a.require(PermWebhooksRead); err != nil {
		return nil, err
	}
	return s.store.Endpoints(ctx, a.OrgID, a.Livemode)
}

// UpdateEndpoint changes an endpoint; enabling it clears its failure
// streak.
func (s *Service) UpdateEndpoint(ctx context.Context, a Actor, endpointID uuid.UUID, in EndpointInput) (*model.WebhookEndpoint, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return nil, err
	}
	if err := a.require(PermWebhooksWrite); err != nil {
		return nil, err
	}
	w, err := s.Endpoint(ctx, a, endpointID)
	if err != nil {
		return nil, err
	}
	if in.URL == "" {
		in.URL = w.URL
	}
	if in.EventTypes == nil {
		in.EventTypes = w.EventTypes
	}
	if err := in.check(a.Livemode); err != nil {
		return nil, err
	}
	w.URL, w.Description, w.EventTypes = strings.TrimSpace(in.URL), strings.TrimSpace(in.Description), in.EventTypes
	if in.Enabled != nil {
		w.Status, w.DisabledReason = "disabled", "Disabled by a person."
		if *in.Enabled {
			w.Status, w.DisabledReason = "enabled", ""
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateEndpoint(ctx, w); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "webhook_endpoint.update", id.Format(id.WebhookEndpoint, w.ID), nil)
	})
	return w, err
}

// RollEndpointSecret replaces an endpoint's signing secret. For overlap
// (at most a week) deliveries are signed with both, so the receiver can
// switch to the new one without rejecting any (ADR 0012).
func (s *Service) RollEndpointSecret(ctx context.Context, a Actor, endpointID uuid.UUID, overlap time.Duration) (string, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return "", err
	}
	if err := a.require(PermWebhooksWrite); err != nil {
		return "", err
	}
	w, err := s.Endpoint(ctx, a, endpointID)
	if err != nil {
		return "", err
	}
	secret := newWebhookSecret()
	sealed, err := s.keys.Encrypt(ctx, a.OrgID, secretAAD(w.ID), []byte(secret))
	if err != nil {
		return "", err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		var keepUntil *time.Time
		if overlap = min(overlap, 7*24*time.Hour); overlap > 0 {
			keepUntil = ptr(s.Now().Add(overlap))
		}
		if err := tx.SetEndpointSecret(ctx, a.OrgID, w.ID, sealed, keepUntil); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "webhook_endpoint.roll_secret", id.Format(id.WebhookEndpoint, w.ID), nil)
	})
	return secret, err
}

// DeleteEndpoint removes an endpoint and its delivery log.
func (s *Service) DeleteEndpoint(ctx context.Context, a Actor, endpointID uuid.UUID) error {
	if err := a.orgWide("webhooks"); err != nil {
		return err
	}
	if err := a.require(PermWebhooksWrite); err != nil {
		return err
	}
	w, err := s.Endpoint(ctx, a, endpointID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteEndpoint(ctx, a.OrgID, w.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "webhook_endpoint.delete", id.Format(id.WebhookEndpoint, w.ID), nil)
	})
}

// Deliveries lists an endpoint's deliveries, newest first.
func (s *Service) Deliveries(ctx context.Context, a Actor, endpointID uuid.UUID, page store.Page) ([]model.Delivery, bool, error) {
	if err := a.orgWide("webhooks"); err != nil {
		return nil, false, err
	}
	if _, err := s.Endpoint(ctx, a, endpointID); err != nil {
		return nil, false, err
	}
	return s.store.Deliveries(ctx, a.OrgID, endpointID, page)
}

// ResendDelivery queues a delivery again now.
func (s *Service) ResendDelivery(ctx context.Context, a Actor, deliveryID uuid.UUID) error {
	if err := a.orgWide("webhooks"); err != nil {
		return err
	}
	if err := a.require(PermWebhooksWrite); err != nil {
		return err
	}
	if err := s.store.ResendDelivery(ctx, a.OrgID, deliveryID, a.Livemode, s.Now()); err != nil {
		return notFound(err, "webhook delivery")
	}
	s.wakeDeliveries(ctx)
	return nil
}

// Events.

// Event returns one event in the actor's mode.
func (s *Service) Event(ctx context.Context, a Actor, eventID uuid.UUID) (*model.Event, error) {
	if err := a.orgWide("events"); err != nil {
		return nil, err
	}
	if err := a.require(PermEventsRead); err != nil {
		return nil, err
	}
	e, err := s.store.Event(ctx, a.OrgID, eventID)
	if err != nil || e.Livemode != a.Livemode {
		return nil, apperr.NotFound("event")
	}
	return e, nil
}

// Events lists events newest first.
func (s *Service) Events(ctx context.Context, a Actor, typ string, page store.Page) ([]*model.Event, bool, error) {
	if err := a.orgWide("events"); err != nil {
		return nil, false, err
	}
	if err := a.require(PermEventsRead); err != nil {
		return nil, false, err
	}
	return s.store.Events(ctx, a.OrgID, a.Livemode, typ, page)
}

// EventsSince returns events after one, oldest first (for streaming).
func (s *Service) EventsSince(ctx context.Context, a Actor, after uuid.UUID, limit int) ([]*model.Event, error) {
	if err := a.orgWide("events"); err != nil {
		return nil, err
	}
	if err := a.require(PermEventsRead); err != nil {
		return nil, err
	}
	return s.store.EventsSince(ctx, a.OrgID, a.Livemode, after, limit)
}

// Delivery.

// signingSecrets are the secrets an endpoint's deliveries are signed with
// at now: its secret, and its previous one while that still overlaps.
func (s *Service) signingSecrets(ctx context.Context, w *model.WebhookEndpoint, now time.Time) ([]string, error) {
	cur, err := s.keys.Decrypt(ctx, w.OrgID, secretAAD(w.ID), w.Secret)
	if err != nil {
		return nil, err
	}
	secrets := []string{string(cur)}
	if w.PreviousSecret != nil && w.PreviousSecretUntil != nil && now.Before(*w.PreviousSecretUntil) {
		prev, err := s.keys.Decrypt(ctx, w.OrgID, secretAAD(w.ID), w.PreviousSecret)
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, string(prev))
	}
	return secrets, nil
}

// otherSignatures adds a v1 signature for each further secret to a
// signature header, as Stripe does while an old secret overlaps a new one.
func otherSignatures(secrets []string, t time.Time, body []byte) string {
	var b strings.Builder
	for _, sec := range secrets {
		_, v1, _ := strings.Cut(Sign(sec, t, body), ",v1=")
		b.WriteString(",v1=" + v1)
	}
	return b.String()
}

// Sign computes the Araldo-Signature header for body at t (Stripe's
// scheme: HMAC-SHA256 over "<unix time>.<body>").
func Sign(secret string, t time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	ts := strconv.FormatInt(t.Unix(), 10)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a signature header, allowing tolerance of clock
// difference. Receivers (and Araldo's own tests and CLI) use it.
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts int64
	var sigs []string
	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return errors.New("signature header is malformed")
	}
	at := time.Unix(ts, 0)
	if now.Sub(at) > tolerance || at.Sub(now) > tolerance {
		return errors.New("signature timestamp is outside the tolerance")
	}
	want := Sign(secret, at, body)
	for _, sig := range sigs {
		if hmac.Equal([]byte("t="+strconv.FormatInt(ts, 10)+",v1="+sig), []byte(want)) {
			return nil
		}
	}
	return errors.New("signature does not match")
}

func (s *Service) wakeDeliveries(ctx context.Context) {
	_, _ = s.store.Pool().Exec(context.WithoutCancel(ctx), "NOTIFY araldo_webhooks")
}

// RunDeliverer delivers webhooks until ctx ends.
func (s *Service) RunDeliverer(ctx context.Context, owner string) error {
	return s.runDeliverer(ctx, owner, nil)
}

// runDeliverer is RunDeliverer for one org, or (orgID nil) every org.
func (s *Service) runDeliverer(ctx context.Context, owner string, orgID *uuid.UUID) error {
	wake := make(chan struct{}, 1)
	go s.listen(ctx, "araldo_webhooks", wake)
	// Up to deliveryBatch deliveries under way, more claimed as each one
	// finishes, so a slow endpoint holds its own slots, not the rest.
	busy := make(chan struct{}, deliveryBatch)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		free := deliveryBatch - len(busy)
		claimed := 0
		if free > 0 && ctx.Err() == nil {
			now := s.Now()
			ds, err := s.store.ClaimDeliveries(ctx, orgID, owner, now, now.Add(deliveryLease), free)
			if err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "claiming webhook deliveries failed", "err", err)
			}
			for _, d := range ds {
				busy <- struct{}{}
				wg.Go(func() {
					defer func() {
						<-busy
						select {
						case wake <- struct{}{}:
						default:
						}
					}()
					s.deliverOne(ctx, owner, d)
				})
			}
			claimed = len(ds)
		}
		if free > 0 && claimed == free {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(publishPoll):
		}
	}
}

// DeliverDue sends one batch of due deliveries in parallel.
func (s *Service) DeliverDue(ctx context.Context, owner string) (int, error) {
	now := s.Now()
	claimed, err := s.store.ClaimDeliveries(ctx, nil, owner, now, now.Add(deliveryLease), deliveryBatch)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, d := range claimed {
		wg.Go(func() { s.deliverOne(ctx, owner, d) })
	}
	wg.Wait()
	return len(claimed), nil
}

// deliverOne sends a claimed delivery, logging what cannot be recorded.
func (s *Service) deliverOne(ctx context.Context, owner string, d store.ClaimedDelivery) {
	if err := s.deliver(ctx, owner, d); err != nil {
		s.log.ErrorContext(ctx, "recording webhook delivery failed", "delivery", id.Format(id.Delivery, d.ID), "err", err)
	}
}

func (s *Service) deliver(ctx context.Context, owner string, d store.ClaimedDelivery) error {
	body, err := json.Marshal(ViewEvent(&d.Event))
	if err != nil {
		return err
	}
	res := store.DeliveryResult{At: s.Now()}
	secrets, err := s.signingSecrets(ctx, &d.Endpoint, res.At)
	if err != nil {
		res.Error = "could not decrypt the endpoint's signing secret"
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint.URL, bytes.NewReader(body))
		if err != nil {
			res.Error = "invalid endpoint URL"
		} else {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Araldo-Webhooks/1 (+https://github.com/spectrum-labs-tech/araldo)")
			req.Header.Set(signatureHeader, Sign(secrets[0], res.At, body)+otherSignatures(secrets[1:], res.At, body))
			req.Header.Set("Araldo-Event-Id", id.Format(id.Event, d.EventID))
			req.Header.Set("Araldo-Delivery-Id", id.Format(id.Delivery, d.ID))
			start := time.Now()
			resp, err := s.HTTP.Do(req)
			res.DurationMS = int(time.Since(start).Milliseconds())
			if err != nil {
				res.Error = truncate(err.Error(), 500)
			} else {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, responseBodyKeep))
				_ = resp.Body.Close()
				res.ResponseStatus, res.ResponseBody = &resp.StatusCode, string(bytes.ToValidUTF8(b, []byte("?")))
				res.Succeeded = resp.StatusCode >= 200 && resp.StatusCode < 300
				if !res.Succeeded {
					res.Error = "HTTP " + strconv.Itoa(resp.StatusCode)
				}
			}
		}
	}
	if !res.Succeeded {
		res.NextAttemptAt = res.At.Add(webhookRetryDelay(d.Attempts))
		res.GiveUp = res.NextAttemptAt.After(d.CreatedAt.Add(deliveryWindow))
	}
	s.metrics.recordDelivery(ctx, &d, &res)
	return s.store.FinishDelivery(context.WithoutCancel(ctx), d, owner, res)
}

// webhookRetryDelay spreads about 15 attempts over three days.
func webhookRetryDelay(attempts int) time.Duration {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour}
	if attempts-1 < len(steps) {
		return steps[max(attempts-1, 0)]
	}
	return 8 * time.Hour
}

// DisableFailingEndpoints turns off endpoints that failed every delivery
// for the whole retry window.
//
// Each one is recorded as a webhook_endpoint.disabled event, in the same
// transaction, which the org's other endpoints receive, and the dashboard
// shows it on the overview until it is fixed and enabled again.
func (s *Service) DisableFailingEndpoints(ctx context.Context) (int, error) {
	var disabled []*model.WebhookEndpoint
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if disabled, err = tx.DisableFailingEndpoints(ctx, s.Now().Add(-deliveryWindow)); err != nil {
			return err
		}
		for _, w := range disabled {
			if err := s.emit(ctx, tx, w.OrgID, w.Livemode, "", "webhook_endpoint.disabled", ViewEndpoint(w)); err != nil {
				return err
			}
		}
		return nil
	})
	for _, w := range disabled {
		s.log.WarnContext(ctx, "webhook endpoint disabled after 3 days of failures", "endpoint", id.Format(id.WebhookEndpoint, w.ID))
	}
	return len(disabled), err
}
