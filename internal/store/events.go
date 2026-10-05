// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Events (ADR 0012).

const eventCols = `id, org_id, livemode, type, data, request_id, created_at`

func scanEvent(r pgx.Row) (*model.Event, error) {
	var e model.Event
	var data []byte
	err := r.Scan(&e.ID, &e.OrgID, &e.Livemode, &e.Type, &data, &e.RequestID, &e.CreatedAt)
	e.Data = data
	return &e, mapErr(err)
}

// CreateEvent stores e and queues a delivery to every enabled endpoint of
// its org and mode that wants its type, in the same transaction.
func (s *Store) CreateEvent(ctx context.Context, e *model.Event) error {
	if _, err := s.q.Exec(ctx, `INSERT INTO events (id, org_id, livemode, type, data, request_id) VALUES ($1, $2, $3, $4, $5, $6)`,
		e.ID, e.OrgID, e.Livemode, e.Type, []byte(e.Data), e.RequestID); err != nil {
		return mapErr(err)
	}
	rows, err := s.q.Query(ctx, `SELECT id FROM webhook_endpoints
		WHERE org_id = $1 AND livemode = $2 AND status = 'enabled' AND ('*' = ANY(event_types) OR $3 = ANY(event_types))`,
		e.OrgID, e.Livemode, e.Type)
	if err != nil {
		return err
	}
	endpoints, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, ep := range endpoints {
		// Delivery IDs are UUIDv7 like every other ID, so they list in order.
		if _, err := s.q.Exec(ctx, `INSERT INTO webhook_deliveries (id, org_id, endpoint_id, event_id, status) VALUES ($1, $2, $3, $4, 'pending')`,
			uuid.Must(uuid.NewV7()), e.OrgID, ep, e.ID); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *Store) Event(ctx context.Context, orgID, id uuid.UUID) (*model.Event, error) {
	return scanEvent(s.q.QueryRow(ctx, `SELECT `+eventCols+` FROM events WHERE org_id = $1 AND id = $2`, orgID, id))
}

// Events lists events newest first, optionally of one type.
func (s *Store) Events(ctx context.Context, orgID uuid.UUID, livemode bool, typ string, page Page) ([]*model.Event, bool, error) {
	where, order, extra := pageClause(page, "id", 4)
	rows, err := s.q.Query(ctx, `SELECT `+eventCols+` FROM events WHERE org_id = $1 AND livemode = $2 AND ($3 = '' OR type = $3)`+where+order,
		append([]any{orgID, livemode, typ}, extra...)...)
	if err != nil {
		return nil, false, err
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Event, error) { return scanEvent(r) })
	if err != nil {
		return nil, false, err
	}
	events, more := trimPage(page, events)
	return events, more, nil
}

// EventsSince returns an org's events after id, oldest first (for streams).
func (s *Store) EventsSince(ctx context.Context, orgID uuid.UUID, livemode bool, after uuid.UUID, limit int) ([]*model.Event, error) {
	rows, err := s.q.Query(ctx, `SELECT `+eventCols+` FROM events WHERE org_id = $1 AND livemode = $2 AND id > $3 ORDER BY id LIMIT $4`,
		orgID, livemode, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Event, error) { return scanEvent(r) })
}

// PruneEvents deletes up to limit events (and their deliveries) older than
// before, oldest first. Event IDs are time-ordered, so the primary key
// finds them without scanning the table.
func (s *Store) PruneEvents(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM events WHERE id IN (SELECT id FROM events WHERE id < $1 ORDER BY id LIMIT $2)`,
		id.Before(before), limit)
	return int(tag.RowsAffected()), err
}

// Webhook endpoints.

const endpointCols = `id, org_id, livemode, url, description, event_types, secret, status, disabled_reason, failing_since, created_at`

func scanEndpoint(r pgx.Row) (*model.WebhookEndpoint, error) {
	var w model.WebhookEndpoint
	err := r.Scan(&w.ID, &w.OrgID, &w.Livemode, &w.URL, &w.Description, &w.EventTypes, &w.Secret, &w.Status, &w.DisabledReason, &w.FailingSince, &w.CreatedAt)
	return &w, mapErr(err)
}

func (s *Store) CreateEndpoint(ctx context.Context, w *model.WebhookEndpoint) error {
	_, err := s.q.Exec(ctx, `INSERT INTO webhook_endpoints (id, org_id, livemode, url, description, event_types, secret, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, w.ID, w.OrgID, w.Livemode, w.URL, w.Description, w.EventTypes, w.Secret, w.Status)
	return mapErr(err)
}

func (s *Store) Endpoint(ctx context.Context, orgID, id uuid.UUID) (*model.WebhookEndpoint, error) {
	return scanEndpoint(s.q.QueryRow(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) Endpoints(ctx context.Context, orgID uuid.UUID, livemode bool) ([]*model.WebhookEndpoint, error) {
	rows, err := s.q.Query(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints WHERE org_id = $1 AND livemode = $2 ORDER BY created_at`, orgID, livemode)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.WebhookEndpoint, error) { return scanEndpoint(r) })
}

func (s *Store) UpdateEndpoint(ctx context.Context, w *model.WebhookEndpoint) error {
	return s.execOne(ctx, `UPDATE webhook_endpoints SET url = $3, description = $4, event_types = $5, status = $6, disabled_reason = $7,
		failing_since = CASE WHEN $6 = 'enabled' THEN NULL ELSE failing_since END, updated_at = now() WHERE org_id = $1 AND id = $2`,
		w.OrgID, w.ID, w.URL, w.Description, w.EventTypes, w.Status, w.DisabledReason)
}

func (s *Store) SetEndpointSecret(ctx context.Context, orgID, id uuid.UUID, secret []byte) error {
	return s.execOne(ctx, `UPDATE webhook_endpoints SET secret = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, secret)
}

func (s *Store) DeleteEndpoint(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM webhook_endpoints WHERE org_id = $1 AND id = $2`, orgID, id)
}

// Deliveries.

// ClaimedDelivery is a delivery leased to a worker, with its event and
// endpoint.
type ClaimedDelivery struct {
	model.Delivery
	Event    model.Event
	Endpoint model.WebhookEndpoint
}

// ClaimDeliveries leases due deliveries to enabled endpoints.
func (s *Store) ClaimDeliveries(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]ClaimedDelivery, error) {
	rows, err := s.q.Query(ctx, `
		WITH picked AS (
			SELECT d.id FROM webhook_deliveries d JOIN webhook_endpoints w ON w.id = d.endpoint_id
			WHERE d.status = 'pending' AND d.next_attempt_at <= $2 AND w.status = 'enabled'
			ORDER BY d.next_attempt_at LIMIT $4 FOR UPDATE OF d SKIP LOCKED
		)
		UPDATE webhook_deliveries d SET status = 'delivering', lease_owner = $1, lease_until = $3, attempts = d.attempts + 1
		FROM picked WHERE d.id = picked.id
		RETURNING d.id`, owner, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err = s.q.Query(ctx, `SELECT d.id, d.org_id, d.endpoint_id, d.event_id, d.status, d.attempts, d.next_attempt_at, d.created_at,
		e.id, e.org_id, e.livemode, e.type, e.data, e.request_id, e.created_at,
		w.id, w.org_id, w.livemode, w.url, w.secret, w.status, w.failing_since
		FROM webhook_deliveries d JOIN events e ON e.id = d.event_id JOIN webhook_endpoints w ON w.id = d.endpoint_id
		WHERE d.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedDelivery, error) {
		var c ClaimedDelivery
		var data []byte
		err := r.Scan(&c.ID, &c.OrgID, &c.EndpointID, &c.EventID, &c.Status, &c.Attempts, &c.NextAttemptAt, &c.CreatedAt,
			&c.Event.ID, &c.Event.OrgID, &c.Event.Livemode, &c.Event.Type, &data, &c.Event.RequestID, &c.Event.CreatedAt,
			&c.Endpoint.ID, &c.Endpoint.OrgID, &c.Endpoint.Livemode, &c.Endpoint.URL, &c.Endpoint.Secret, &c.Endpoint.Status, &c.Endpoint.FailingSince)
		c.Event.Data = data
		return c, err
	})
}

// DeliveryResult is how one delivery attempt went.
type DeliveryResult struct {
	Succeeded      bool
	GiveUp         bool
	NextAttemptAt  time.Time
	ResponseStatus *int
	ResponseBody   string
	Error          string
	DurationMS     int
	At             time.Time
}

// FinishDelivery records an attempt and updates the endpoint's failure
// streak.
func (s *Store) FinishDelivery(ctx context.Context, d ClaimedDelivery, owner string, r DeliveryResult) error {
	status := "pending"
	switch {
	case r.Succeeded:
		status = "succeeded"
	case r.GiveUp:
		status = "failed"
	}
	if err := s.execOne(ctx, `UPDATE webhook_deliveries SET status = $3, next_attempt_at = $4, response_status = $5, response_body = $6,
		error = $7, duration_ms = $8, last_attempt_at = $9, lease_owner = NULL, lease_until = NULL
		WHERE id = $1 AND lease_owner = $2 AND status = 'delivering'`,
		d.ID, owner, status, r.NextAttemptAt, r.ResponseStatus, r.ResponseBody, r.Error, r.DurationMS, r.At); err != nil {
		return err
	}
	if r.Succeeded {
		_, err := s.q.Exec(ctx, `UPDATE webhook_endpoints SET failing_since = NULL WHERE id = $1 AND failing_since IS NOT NULL`, d.EndpointID)
		return err
	}
	_, err := s.q.Exec(ctx, `UPDATE webhook_endpoints SET failing_since = COALESCE(failing_since, $2) WHERE id = $1`, d.EndpointID, r.At)
	return err
}

// ReclaimDeliveries returns deliveries whose worker vanished to pending.
func (s *Store) ReclaimDeliveries(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `UPDATE webhook_deliveries SET status = 'pending', lease_owner = NULL, lease_until = NULL, next_attempt_at = $1
		WHERE status = 'delivering' AND lease_until < $1`, now)
	return int(tag.RowsAffected()), err
}

// DisableFailingEndpoints disables endpoints failing since before cutoff
// and returns them.
func (s *Store) DisableFailingEndpoints(ctx context.Context, cutoff time.Time) ([]*model.WebhookEndpoint, error) {
	rows, err := s.q.Query(ctx, `UPDATE webhook_endpoints SET status = 'disabled',
		disabled_reason = 'Every delivery failed for 3 days.', updated_at = now()
		WHERE status = 'enabled' AND failing_since < $1 RETURNING `+endpointCols, cutoff)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.WebhookEndpoint, error) { return scanEndpoint(r) })
}

// Deliveries lists an endpoint's deliveries, newest first.
func (s *Store) Deliveries(ctx context.Context, orgID, endpointID uuid.UUID, page Page) ([]model.Delivery, bool, error) {
	where, order, extra := pageClause(page, "d.id", 3)
	rows, err := s.q.Query(ctx, `SELECT d.id, d.org_id, d.endpoint_id, d.event_id, d.status, d.attempts, d.next_attempt_at, d.response_status,
		d.response_body, d.error, d.duration_ms, d.last_attempt_at, d.created_at, e.type
		FROM webhook_deliveries d JOIN events e ON e.id = d.event_id WHERE d.org_id = $1 AND d.endpoint_id = $2`+where+order,
		append([]any{orgID, endpointID}, extra...)...)
	if err != nil {
		return nil, false, err
	}
	ds, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Delivery, error) {
		var d model.Delivery
		err := r.Scan(&d.ID, &d.OrgID, &d.EndpointID, &d.EventID, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.ResponseStatus,
			&d.ResponseBody, &d.Error, &d.DurationMS, &d.LastAttemptAt, &d.CreatedAt, &d.EventType)
		return d, err
	})
	if err != nil {
		return nil, false, err
	}
	ds, more := trimPage(page, ds)
	return ds, more, nil
}

// ResendDelivery queues a delivery again now.
func (s *Store) ResendDelivery(ctx context.Context, orgID, id uuid.UUID, now time.Time) error {
	return s.execOne(ctx, `UPDATE webhook_deliveries SET status = 'pending', next_attempt_at = $3
		WHERE org_id = $1 AND id = $2 AND status IN ('succeeded', 'failed', 'pending')`, orgID, id, now)
}

// Idempotency keys (ADR 0005).

// IdempotencyRecord is a stored request outcome.
type IdempotencyRecord struct {
	Fingerprint    []byte
	Status         string
	ResponseStatus int
	ResponseBody   []byte
	CreatedAt      time.Time
}

// BeginIdempotent claims key for a request. If the key exists, it returns
// the stored record and claimed=false, unless the same request claimed it
// before abandoned (still in progress from before abandoned, its process
// gone), when this request takes it over.
func (s *Store) BeginIdempotent(ctx context.Context, apiKeyID uuid.UUID, key string, fingerprint []byte, since, abandoned time.Time) (*IdempotencyRecord, bool, error) {
	// Forget an expired record first, so the key can be reused.
	if _, err := s.q.Exec(ctx, `DELETE FROM idempotency_keys WHERE api_key_id = $1 AND key = $2 AND created_at < $3`, apiKeyID, key, since); err != nil {
		return nil, false, err
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO idempotency_keys (api_key_id, key, fingerprint, status) VALUES ($1, $2, $3, 'in_progress')
		ON CONFLICT DO NOTHING`, apiKeyID, key, fingerprint)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	tag, err = s.q.Exec(ctx, `UPDATE idempotency_keys SET created_at = now()
		WHERE api_key_id = $1 AND key = $2 AND fingerprint = $3 AND status = 'in_progress' AND created_at < $4`,
		apiKeyID, key, fingerprint, abandoned)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	var rec IdempotencyRecord
	var status *int
	err = s.q.QueryRow(ctx, `SELECT fingerprint, status, response_status, response_body, created_at FROM idempotency_keys
		WHERE api_key_id = $1 AND key = $2`, apiKeyID, key).Scan(&rec.Fingerprint, &rec.Status, &status, &rec.ResponseBody, &rec.CreatedAt)
	if status != nil {
		rec.ResponseStatus = *status
	}
	return &rec, false, mapErr(err)
}

// FinishIdempotent stores a response; with status 0 it releases the key
// (the request failed in a way worth retrying).
func (s *Store) FinishIdempotent(ctx context.Context, apiKeyID uuid.UUID, key string, status int, body []byte) error {
	if status == 0 {
		_, err := s.q.Exec(ctx, `DELETE FROM idempotency_keys WHERE api_key_id = $1 AND key = $2 AND status = 'in_progress'`, apiKeyID, key)
		return err
	}
	_, err := s.q.Exec(ctx, `UPDATE idempotency_keys SET status = 'done', response_status = $3, response_body = $4
		WHERE api_key_id = $1 AND key = $2`, apiKeyID, key, status, body)
	return err
}

func (s *Store) PruneIdempotencyKeys(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, before)
	return int(tag.RowsAffected()), err
}

// Audit log (ADR 0004).

func (s *Store) RecordAudit(ctx context.Context, a *model.AuditEvent) error {
	if a.Detail == nil {
		a.Detail = map[string]any{}
	}
	if a.Outcome == "" {
		a.Outcome = "ok"
	}
	_, err := s.q.Exec(ctx, `INSERT INTO audit_events (id, org_id, actor_user, actor_key, action, target, outcome, request_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		a.ID, a.OrgID, a.ActorUser, a.ActorKey, a.Action, a.Target, a.Outcome, a.RequestID, a.Detail)
	return mapErr(err)
}

// AuditEvents lists an org's audit trail, newest first.
func (s *Store) AuditEvents(ctx context.Context, orgID uuid.UUID, page Page) ([]model.AuditEvent, bool, error) {
	where, order, extra := pageClause(page, "a.id", 2)
	rows, err := s.q.Query(ctx, `SELECT a.id, a.org_id, a.actor_user, a.actor_key, a.action, a.target, a.outcome, a.request_id, a.detail, a.created_at
		FROM audit_events a WHERE a.org_id = $1`+where+order, append([]any{orgID}, extra...)...)
	if err != nil {
		return nil, false, err
	}
	evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.AuditEvent, error) {
		var a model.AuditEvent
		err := r.Scan(&a.ID, &a.OrgID, &a.ActorUser, &a.ActorKey, &a.Action, &a.Target, &a.Outcome, &a.RequestID, &a.Detail, &a.CreatedAt)
		return a, err
	})
	if err != nil {
		return nil, false, err
	}
	evs, more := trimPage(page, evs)
	return evs, more, nil
}
