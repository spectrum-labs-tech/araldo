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
- **too_much_media**: more images than the platform (or a post, at most
  ten) takes.
- **media_too_large**, **media_type_unsupported**, **media_aspect_ratio**,
  **media_dimensions**: an image breaks a platform's rules. `detail` names
  the channel and the media; `/v1/platforms` lists each platform's `images`
  limits.
- **media_missing**, **media_other_brand**, **media_duplicate**,
  **media_invalid**: a listed media ID cannot be used.
- **threads_unsupported**: several parts were given to a platform without
  threads.
- **empty**: the rendered text is empty.
- **variable_missing**: the template uses a field the data does not have.
- **data_invalid**: the data does not match the template's JSON Schema.
- **example_required**: a preview without data needs the template to have
  an example that matches its variables.
- **template_syntax**, **body_missing**, **variables_invalid**,
  **example_invalid**: the template itself is not valid.
- **no_channels**: the brand has no active channels in this mode.
- **channel_missing**, **channel_inactive**, **channel_other_brand**: a
  listed channel cannot be used.
- **no_slots**, **slots_full**: `next_slot` found nothing.
- **simulation_in_live_mode**, **simulation_invalid**:
  `metadata.araldo_simulate` is for test mode, with a known value.

## Engagement

- **group_by_invalid**: group a summary by `post`, `channel` or `template`.
- **window_invalid**: `since` must be before `until`, at most a year apart.

## Media

- **file_missing** (400): upload the image as `multipart/form-data` in a
  part named `file`, or send JSON with a `url`.
- **form_invalid** (400): the multipart form could not be read.
- **media_empty**, **media_too_large**: the file is empty, or over 16 MiB.
- **media_type_unsupported**: not a JPEG, PNG, GIF or WebP image (the
  bytes decide, not the file name).
- **alt_too_long**: alt text is at most 1,000 characters.
- **url_invalid**, **url_unreachable**: the `url` is not http(s), or could
  not be fetched (private addresses are refused).
- **media_in_use** (409): a post uses this media, so it cannot be deleted.

## Channels

- **livemode_required**: test mode connects only sandbox channels.
- **testmode_required**: sandbox channels exist only in test mode.
- **connect_failed**: the platform refused the credentials.
- **provider_unavailable**: the platform could not be reached; try again.
