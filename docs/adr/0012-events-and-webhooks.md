# ADR 0012: Events are written in the same transaction; webhooks are signed and retried

- Status: proposed
- Date: 2026-09-28

## Context

Callers need to know what happened to what they scheduled: the permalink of
a published post (to show "shared on X" beside the thing announced), a failure
to act on, a channel that needs reconnecting. Polling for this is wasteful
and slow. Webhooks are only trustworthy if they are never lost, can be
verified, and can be replayed.

## Decision

1. **Every notable change writes an event** in the same transaction as the
   change: `post.created`, `post.approved`, `post.published`,
   `post.failed`, `post_target.published`, `post_target.failed`,
   `post_target.uncertain`, `channel.connected`, `channel.needs_reauth`,
   `template.version_published`, `approval.requested`, and more as features
   arrive. An event is
   `{ id: "evt_…", object: "event", type, created_at, livemode, request_id, data: { object: <snapshot> } }`.
   Events are kept for 30 days and listed at `GET /v1/events`.
2. **Webhook endpoints** belong to an org and a mode. Each has a URL, the
   event types it wants (or `*`), and a signing secret `whsec_…` (encrypted,
   [ADR 0008](0008-encryption.md)). Live endpoints must use HTTPS.
3. **Deliveries** are outbox rows, one per event and endpoint:
   - a `POST` of the event JSON, with a 10-second timeout, where any 2xx
     counts as success;
   - retries back off exponentially for up to 3 days;
   - an endpoint that has failed for 3 days straight is disabled, its admins
     are emailed, and an event records it;
   - every attempt is logged (status, latency, response code, the first
     1 KiB of the response body) and can be resent from the API or
     dashboard.
4. **Signatures** follow Stripe's scheme:
   `Araldo-Signature: t=<unix time>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`.
   Receivers reject timestamps more than 5 minutes old. Rolling a secret
   keeps the old one signing (a second `v1`) for up to 24 hours. Each
   request also carries `Araldo-Event-Id` and `Araldo-Delivery-Id`.
5. **Order is not guaranteed and delivery is at least once.** Receivers
   deduplicate on the event ID; the docs and SDKs say so and ship a
   signature verifier.
6. **No server-side request forgery.** Deliveries refuse loopback, private
   and link-local addresses, checked when connecting (so DNS rebinding
   cannot get around it). A self-hoster can allow private addresses with
   `ARALDO_WEBHOOK_ALLOW_PRIVATE=true`.
7. **`araldo listen`** (like `stripe listen`) streams the org's events over an
   authenticated server-sent events endpoint (`GET /v1/events/stream`) and
   forwards each one, signed, to a local URL. It works in test mode by
   default.

## Alternatives considered

- **Sending webhooks directly from the code that made the change.** A crash
  between the commit and the send loses the event.
- **Unsigned webhooks with a secret in the URL.** Secrets in URLs end up in
  logs and cannot be rotated without downtime.

## Consequences

- Event and delivery tables grow quickly; pruning is an `opsched` task.
- Webhook receivers must be idempotent. That is the industry norm, and the
  docs make it explicit.
