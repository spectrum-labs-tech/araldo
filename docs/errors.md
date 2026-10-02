# Error codes

Every API error is a problem detail with a stable `code`
([ADR 0005](adr/0005-api-conventions.md)). Validation errors (HTTP 422) list
every problem in `errors`, each with its own `code` and `param`.

## Authentication and limits

- **api_key_missing** (401): send `Authorization: Bearer ald_test_…`.
- **api_key_invalid** (401): the key is malformed or unknown.
- **api_key_expired** (401): the key was revoked or has expired.
- **scope_missing** (403): a restricted key lacks the scope this needs.
  Administrative scopes (`keys:write`, `posts:approve`, `audit:read`) are
  held only when a key lists them.
- **forbidden** (403): the caller may not do this.
- **rate_limited** (429): slow down; see `Retry-After`.

## Requests

- **json_invalid**, **parameter_unknown**, **parameter_invalid**,
  **body_too_large** (400): the request itself is malformed. An unknown
  query parameter is refused like an unknown body field, so a misspelled
  filter never passes silently.
- **resource_missing** (404): no such object in this org and mode. Paths
  take IDs (and a brand's slug); find a template by key with
  `GET /v1/templates?brand=…&key=…`.
- **idempotency_key_reused** (409): the key was used with a different
  request that succeeded. A request that failed does not keep its key, so
  it can be sent again, corrected, with the same key.
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
- **variable_missing**: the template uses a field that its schema does not
  declare and the data does not have. A field the schema declares but the
  data leaves out is null, so `{{if .field}}…{{end}}` and
  `{{with .field}}…{{end}}` handle optional fields.
- **variable_unguarded**: the template prints an optional field the data
  leaves out; wrap it in `{{if}}` or `{{with}}`.
- **data_invalid**: the data does not match the template's JSON Schema.
- **example_required**: a preview without data needs the template to have
  an example that matches its variables.
- **template_syntax**, **body_missing**, **variables_invalid**,
  **example_invalid**: the template itself is not valid.
- **no_channels**: the brand has no active channels in this mode.
- **channel_missing**, **channel_inactive**, **channel_other_brand**: a
  listed channel cannot be used.
- **no_slots**, **slots_full**: `next_slot` found nothing. Set the brand's
  `slots` with `POST /v1/brands/{id}`. A post waiting for approval takes
  its slot when approved, so approval can fail with `slots_full` too; the
  post stays pending.
- **no_slot_before_publish_by**: no free slot comes before the post's
  `publish_by`. Give a later deadline, or move the post.
- **slot_taken**: moving a post onto a slot another post holds; the
  problem's `detail.post` names it. Move that post, or swap with it.
- **post_not_movable**: only posts that are `scheduled` or
  `pending_approval`, with no attempts yet, can be moved.
- **post_unscheduled**, **swap_other_brand**: a swap needs two posts of
  the same brand, both with a time; a post waiting for approval with
  `next_slot` has none yet.
- **reschedule_invalid**: give `publish_at` or `swap_with` (and no
  `publish_by` with a swap).
- **slot_invalid**, **too_many_slots**: a slot is a weekday (`monday`) and a
  24-hour time (`09:00`); a brand has at most 200.
- **simulation_in_live_mode**, **simulation_invalid**:
  `metadata.araldo_simulate` is for test mode, with a known value.

## Engagement

- **group_by_invalid**: group a summary by `post`, `channel` or `template`.
- **window_invalid**: `since` must be before `until`, at most a year apart.

## API keys

- **scope_not_grantable**: only a member can create a key with an
  administrative scope.
- **scope_not_held**: a key can only create (or roll) keys with scopes it
  holds itself; with no scopes listed, the new key would hold them all.
- **brand_required**: a key limited to one brand creates keys for that
  brand only.
- **livemode_mismatch**: a key creates keys in its own mode only.
- **key_inactive** (409): the key to roll has expired or was revoked.

## Connecting with OAuth

- **connect_expired**: the sign-in took over 15 minutes or was already
  used; start again.
- **connect_failed**: the platform refused the sign-in or the app's
  credentials.
- **connect_empty**, **choice_required**: there was no account to connect,
  or none was chosen.
- **provider_invalid**, **client_id_invalid**, **client_secret_invalid**,
  **name_taken**: the developer app is not valid.
- A post target fails with **media_link_missing** when a platform that
  fetches images (Threads) cannot reach the install's API.

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
