# ADR 0005: A contract-first API with Stripe-style conventions

- Status: proposed
- Date: 2026-09-28

## Context

The API is the product ([ADR 0001](0001-scope.md)). Developers judge it in
the first ten minutes: can they guess an endpoint, retry safely, page
through a list, and understand an error without opening the docs? Stripe
set the bar most developers now expect.

## Decision

1. **Contract first.** `api/openapi.yaml` (OpenAPI 3.0.3) is the source of
   truth, and SDKs are generated from it. Handlers are written by hand on
   the standard library router and return `core`'s view types; a test
   fails if a route exists in the server but not in the contract, or the
   other way round. The contract is served at `/v1/openapi.yaml`.
2. **Versioning.** Base path `/v1`. Within v1 changes are additive only:
   new endpoints, new optional fields, new enum values (clients must
   tolerate unknown ones). A breaking change needs `/v2`, and `/v1` stays
   until a published deprecation date.
3. **Identifiers** are prefixed and time-sortable: the TypeID format, a type
   prefix plus a UUIDv7 in base32 (`post_01j9x3…`). Postgres stores the
   UUID; the prefix is added and checked at the API edge, so passing a
   template ID where a post ID belongs fails with a clear error. Prefixes:
   `org`, `brand`, `user`, `mem`, `chan`, `papp` (provider app), `tmpl`,
   `post`, `ptgt` (post target), `media`, `evt`, `whep` (webhook endpoint),
   `whdel` (delivery), `key`, `req`.
4. **Objects** carry `id`, `object` (`"post"`), `created_at`, `livemode`,
   and `metadata`: up to 50 string key–value pairs the caller owns (for
   example ar15.build's build ID), returned everywhere and filterable in
   lists.
5. **Idempotency.** Every `POST` accepts an `Idempotency-Key` header. For 24
   hours Araldo stores the key per API key and mode with a fingerprint of
   the request and the response:
   - a retry with the same key and request returns the stored response,
     with `Idempotent-Replayed: true`;
   - the same key with a different request is `409 idempotency_key_reused`;
   - a retry while the first request is still running is
     `409 idempotency_key_in_use`, which clients may retry.
6. **Pagination** is by cursor: `limit` (1–100, default 20),
   `starting_after` or `ending_before` (an object ID). A list is
   `{ "object": "list", "data": [...], "has_more": true }`. There are no
   totals, so lists stay fast at any size.
7. **Errors** are RFC 9457 problem details (`application/problem+json`),
   extended with:
   - `code`, a stable machine-readable string such as
     `template_variable_missing`;
   - `param`, the field at fault;
   - `request_id` and `doc_url`.

   Validation failures list every problem in `errors[]`, each with its own
   `code` and `param`. Status codes are 400 (malformed), 401, 403, 404,
   409, 422 (valid JSON, invalid request), 429 and 5xx.
8. **Every response has a `Request-Id`** (`req_…`). It is logged and shown
   in the dashboard's request log, so "it failed" becomes "request
   req_01j9… failed".
9. **Authentication:** `Authorization: Bearer ald_live_…` ([ADR 0006](0006-test-mode-and-api-keys.md)).
   The API never accepts session cookies, so it has no CSRF exposure; the
   dashboard calls services directly ([ADR 0015](0015-dashboard.md)).
10. **Rate limits** are per API key, advertised with `RateLimit-Limit`,
    `RateLimit-Remaining` and `RateLimit-Reset`; exceeding them returns 429
    with `Retry-After`.
11. **Times** are RFC 3339. Responses are in UTC; requests may carry any
    offset. Schedules that mention local times ("9am") use the brand's IANA
    time zone.
12. **No `expand` in v1.** Small child objects (a post's targets) are
    embedded; everything else is referenced by ID.

## Alternatives considered

- **Code-first with a generated spec.** Faster to start, but the spec drifts
  toward whatever the code happens to do.
- **Generated server code** (`oapi-codegen`, as caseline does). It would
  guarantee request and response shapes too, but the idempotency, error and
  pagination conventions are easier to apply uniformly in hand-written
  handlers. Revisit if the contract and handlers start to disagree.
- **GraphQL.** Flexible reads, but idempotent writes, webhooks, caching and
  a clean CLI are all harder, and most integrations here are "create one
  post".
- **Offset pagination with totals** (caseline). Simple, but slow on large
  tables, and rows shift between pages under concurrent writes.

## Consequences

- Adding an endpoint: edit the YAML, add the handler and its route, add
  tests. The route test catches a forgotten half.
- Idempotency keys and request logs need tables and a pruning task.
- IDs need a small `internal/id` package with round-trip and fuzz tests.
