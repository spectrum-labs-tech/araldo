# Error codes

Every API error is a problem detail with a stable `code`
([ADR 0005](adr/0005-api-conventions.md)). Validation errors (HTTP 422) list
every problem in `errors`, each with its own `code` and `param`.

## Authentication and limits

- **api_key_missing** (401): send `Authorization: Bearer ald_test_…`.
- **api_key_invalid** (401): the key is malformed or unknown.
- **api_key_expired** (401): the key was revoked or has expired.
- **scope_missing** (403): a restricted key lacks the scope this needs.
- **forbidden** (403): the caller may not do this.
- **rate_limited** (429): slow down; see `Retry-After`.

## Requests

- **json_invalid**, **parameter_unknown**, **parameter_invalid**,
  **body_too_large** (400): the request itself is malformed.
- **resource_missing** (404): no such object in this org and mode.
- **idempotency_key_reused** (409): the key was used with a different
  request.
- **idempotency_key_in_use** (409): the first request with this key is still
  running; retry shortly.

## Posts and templates

- **too_long**: text exceeds a platform's limit. `detail` has the channel,
  part, measured length and limit. Shorten it, or set `fit` to `truncate`
  or `thread`.
- **media_required**: the platform needs an image or video.
- **threads_unsupported**: several parts were given to a platform without
  threads.
- **empty**: the rendered text is empty.
- **variable_missing**: the template uses a field the data does not have.
- **data_invalid**: the data does not match the template's JSON Schema.
- **template_syntax**, **body_missing**, **variables_invalid**,
  **example_invalid**: the template itself is not valid.
- **no_channels**: the brand has no active channels in this mode.
- **channel_missing**, **channel_inactive**, **channel_other_brand**: a
  listed channel cannot be used.
- **no_slots**, **slots_full**: `next_slot` found nothing.
- **simulation_in_live_mode**, **simulation_invalid**:
  `metadata.araldo_simulate` is for test mode, with a known value.

## Channels

- **livemode_required**: test mode connects only sandbox channels.
- **testmode_required**: sandbox channels exist only in test mode.
- **connect_failed**: the platform refused the credentials.
- **provider_unavailable**: the platform could not be reached; try again.
