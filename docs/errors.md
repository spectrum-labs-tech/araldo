# Error codes

Every API error is a problem detail with a stable `code`
([ADR 0005](adr/0005-api-conventions.md)). Validation errors (HTTP 422) list
every problem in `errors`, each with its own `code` and `param`.

## Authentication and limits

- **api_key_missing** (401): send `Authorization: Bearer ald_test_…`.
- **api_key_invalid** (401): the key is malformed or unknown.
- **api_key_expired** (401): the key was revoked or has expired.
- **scope_missing** (403): a restricted key lacks the scope this needs.
  Administrative scopes (`keys:write`, `posts:approve`, `audit:read`,
  `ads:write`) are held only when a key lists them.
- **forbidden** (403): the caller may not do this.
- **org_read_only** (403): the install's operator made the org read-only:
  it can read, but not change anything. The dashboard says why.
- **org_suspended** (403): the install's operator suspended the org; its
  keys and tokens are refused until it is active again.
- **limit_reached** (403): the org has reached one of its limits (the
  `param` names it: `brands`, `channels`, `members` or `posts_per_month`).
- **operator_key_invalid** (401), **operator_key_revoked** (401): the
  operator API needs a valid operator key (`ald_op_…`).
- **external_ref_taken** (409): another org has that external reference.
- **user_token_invalid** (401): the CLI token was revoked, has not been
  used in a year, or does not exist; run `araldo auth login` again.
- **sso_required** (403): the org requires single sign-on, and the CLI
  token was not approved from a session that came through it: run
  `araldo auth login` again and approve it after signing in with single
  sign-on.
- **org_required** (422), **org_ambiguous** (422): a person in several orgs
  names one in the `Araldo-Org` header, by ID when two share a name.
- **org_unknown** (404): the person does not belong to the org named in
  `Araldo-Org`.
- **reauthentication_required** (403): inviting, changing or removing an
  owner, or turning off required two-factor authentication, needs a recent
  password confirmation; do it in the dashboard.
- **rate_limited** (429): slow down; see `Retry-After`.

## Availability

- **database_unavailable** (503): the database cannot be reached; retry
  after `Retry-After`.
- **keys_unavailable** (503): stored credentials cannot be read right now,
  because no master key is configured or its key service is unreachable.
  Retry later.
- **service_unavailable** (503): another service Araldo depends on cannot
  be reached; retry later.
- **internal_error** (500): something failed inside Araldo; retry, and
  report it if it persists.

## Requests

- **route_unknown** (404): no route has this path; the routes are in
  `/v1/openapi.yaml`. **method_not_allowed** (405): the path takes other
  methods, listed in `Allow`.
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
- **idempotency_key_invalid** (422): an `Idempotency-Key` is at most 255
  characters.
- **body_unreadable** (400): the request body could not be read; send it
  again.
- **parameter_missing** (400): a required parameter is absent; `param`
  names it.
- **parameter_conflict** (400): give `starting_after` or `ending_before`,
  not both.
- **name_invalid** (422): a name (of an org, brand, key or developer app)
  is empty or over 100 characters.

## Brands

- **slug_invalid**: slugs use lowercase letters, digits and dashes.
- **slug_taken**: another brand in the org has that slug; choose another.
- **timezone_invalid**: `timezone` is an IANA time zone, like
  `Europe/Rome`.
- **approval_policy_invalid**: `approval_policy` is `none`,
  `required_for_editors_and_keys` or `required_for_all`.
- **utm_domain_invalid**, **utm_domains_too_many**: `utm_domains` lists
  domains like `example.com`, at most 20.

## Posts and templates

- **publish_at_invalid**: `publish_at` is not `"now"`, `"next_slot"` or an
  RFC 3339 time, is more than a year ahead, or is more than 15 minutes in
  the past (use `"now"` to publish right away).
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
- **content_missing**, **content_conflict**: a post takes a `template`
  (with `data`) or `content`: one of them, not both.
- **content_empty**: `content` needs a body, parts or media.
- **fit_invalid**: each platform's `fit` is `error`, `truncate` or
  `thread`.
- **metadata_invalid**: metadata holds at most 50 keys, each 1 to 40
  characters, with values of at most 500.
- **channel_invalid**: an entry in `channels` is not a channel ID.
- **publish_by_invalid**: `publish_by` must be in the future and after
  `publish_at`.
- **thread_too_long**: more parts than the platform takes in a thread.
- **video_alone**: a post with a video carries no other media.
- **video_unsupported**: Araldo does not post video to that platform yet.
- **video_too_short**, **video_too_long**, **video_frame_rate**,
  **video_codec_unsupported**: a video breaks a platform's rules. `detail`
  names the channel and the media; export unsupported codecs as H.264.
- **slot_contention** (409): other posts kept taking the free slot; retry.
- **post_not_cancelable** (409): nothing is left to cancel: every target
  is already canceled, published, failed or publishing.
- **post_not_pending** (409): only a post waiting for approval can be
  approved or rejected.
- **target_not_resolvable** (409): only a failed or uncertain target can
  be retried or marked published.
- **query_too_long**: search (`q`) for at most 200 characters.
- **key_invalid**, **key_taken**: a template key uses lowercase letters,
  digits, dashes and underscores, and is unique in its brand.
- **approval_invalid**: a template's `approval` is `inherit`, `required`
  or `not_required`.
- **template_missing**, **template_invalid**: the brand has no such
  template, or the reference is not `key`, `key@3` or a template ID.
- **provider_unknown**: an override, `fit` or preview names a platform
  Araldo does not know.
- **data_too_large**, **output_too_large**: template data, and the text it
  renders, are each at most 64 KiB.
- **render_failed**: the template failed while rendering this data; the
  message says where.

## Engagement

- **group_by_invalid**: group a summary by `post`, `channel` or `template`.
- **window_invalid**: `since` must be before `until`, at most a year apart.

## Reports

- **month_invalid**: `month` is `YYYY-MM`.
- **period_invalid**: `since` must be on or before `until`, at most a year
  apart, and the period must have started.

## Ads

- **network_unsupported**: this install cannot read that network.
- **livemode_required**, **testmode_required**: test mode connects only
  sandbox ad accounts, and live mode only real ones.
- **ad_account_connected**: that account is already connected to the brand.
- **group_by_invalid**: group an ads summary by `brand`, `account`,
  `campaign` or `day`.
- **window_invalid**: `since` must be on or before `until`, at most a year
  apart.

## Web analytics

- **provider_unsupported**: this install cannot read that analytics tool.
- **analytics_source_connected**: that site is already connected to the
  brand.
- **goal_invalid**, **goals_too_many**: goal names are at most 120
  characters, and a source has at most 20.
- **group_by_invalid**: group a summary by `post`, `source`, `medium`,
  `campaign`, `content` or `day`.

## API keys

- **scope_not_grantable**: only a member can create a key with an
  administrative scope.
- **scope_not_held**: a key can only create (or roll) keys with scopes it
  holds itself; with no scopes listed, the new key would hold them all.
- **brand_required**: a key limited to one brand creates keys for that
  brand only.
- **livemode_mismatch**: a key creates keys in its own mode only.
- **expires_too_late**: a key that expires creates keys that expire no
  later than it does.
- **scope_invalid**: a listed scope does not exist.
- **brand_invalid**: `brand` is not one of the org's brands.
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
- **media_unreadable**: the video cannot be read; export it as an MP4
  (H.264 and AAC).
- **video_needs_s3**: this install cannot store video until its operator
  configures S3-compatible storage.

## Channels

- **livemode_required**: test mode connects only sandbox channels.
- **testmode_required**: sandbox channels exist only in test mode.
- **connect_failed**: the platform refused the credentials.
- **provider_unavailable**: the platform could not be reached; try again.
- **field_required**, **field_unknown**: name the `provider`, and give
  each field it requires in `fields` and no others (ad accounts, analytics
  sources and mail accounts check their `fields` the same way).
- **needs_reauth** (409): a channel that needs reconnecting cannot be
  enabled; reconnect it first.

## Newsletters

- **from_name_invalid**, **from_email_invalid**, **reply_to_invalid**: a
  sender needs a name of 1 to 100 characters and a valid email address,
  and a reply-to, if given, must be one too.
- **mail_account_connected** (409): the brand already sends as that
  address through that account.
- **mail_account_in_use** (409): scheduled issues go through this account;
  cancel them first.
- **mail_account_unknown**: the mail account is not one of the brand's in
  this mode, or was disconnected; edit the issue.
- **mail_account_needs_reauth**: connect the mail account again.
- **audience_unknown**: the provider has no audience with that ID.
- **audience_missing**: a delivery needs at least one audience, given or
  set as the account's defaults.
- **delivery_duplicate**: list each mail account once in `deliveries`,
  with all its audiences.
- **subject_invalid**, **preview_text_too_long**: a subject is 1 to 200
  characters, and preview text at most 200.
- **body_invalid**: the body is 1 to 100,000 bytes and must render; the
  message gives the line at fault.
- **accent_invalid**, **postal_address_too_long**, **footer_invalid**,
  **logo_unknown**: the email theme is not valid. The accent is a color
  like `#1d4ed8` dark enough to read on white, the postal address at most
  300 characters, the footer one line of at most 300, and the logo one of
  the brand's images.
- **postal_address_missing**, **deliveries_missing**: scheduling needs a
  postal address in the brand's email theme, and at least one delivery.
- **send_at_invalid**: schedule an issue at least 2 minutes and at most a
  year ahead.
- **to_invalid**: send a test to 1 to 5 valid email addresses.
- **issue_not_draft** (409): only a draft can be edited or scheduled;
  unschedule it first, or reload it if it changed meanwhile.
- **issue_not_schedulable** (409): the issue's status cannot be
  scheduled.
- **issue_not_scheduled** (409): only a scheduled issue can be
  unscheduled.
- **issue_not_cancelable** (409): the issue has finished and cannot be
  stopped.
- **issue_partly_sent** (409): some copies were sent, so it cannot go back
  to draft; cancel the rest instead.
- **issue_not_pending** (409): only an issue waiting for approval can be
  approved or rejected.
- **handoff_in_progress** (409): the issue is being handed to its
  providers right now; retry in a minute.

## Webhooks

- **url_invalid**, **url_insecure**: an endpoint is an absolute http(s)
  URL with no credentials in it, and HTTPS in live mode.
- **event_type_invalid**: `enabled_events` lists types from the
  `EventType` enum in `/v1/openapi.yaml`, or `"*"` for all.
- **description_invalid**: a description is at most 500 characters.

## Members and org

- **role_invalid**: a role is `owner`, `admin`, `editor` or `viewer`.
- **email_invalid**: the email address is not valid.
- **last_owner** (409): an org keeps at least one owner.
- **mfa_required_first**: turn on two-factor authentication for yourself
  before requiring it for the org.

## Single sign-on (dashboard)

- **issuer_invalid**, **issuer_unreachable**: the issuer is the provider's
  `https://` URL, serving `/.well-known/openid-configuration`.
- **client_id_invalid**, **client_secret_required**: the provider's client
  ID and, the first time, its secret.
- **default_role_invalid**: people who join get `admin`, `editor` or
  `viewer`, never `owner`.
- **domain_invalid**, **domain_exists**, **domains_limit**: a domain name
  such as `araldo.dev`, once per org, 20 at most.
- **domain_unverified**: the TXT record at `_araldo-verify.<domain>` does
  not hold `araldo-verify=<token>` yet.
- **domain_taken** (409): another org verified the domain.
- **sso_not_connected**, **sso_no_domain**, **sso_session_required**:
  requiring single sign-on needs a provider, a verified domain, and that
  you signed in through it.
- **sso_required** (409): while it is required, the provider and the last
  verified domain stay.
- **sso_unavailable**: no org signs in with that email's domain.
- **sso_session** (403): a session through single sign-on cannot change
  how the person signs in; sign in with a password or passkey for that.
- **sso_expired**, **sso_failed**, **sso_email_unverified**,
  **sso_email_missing**, **sso_domain_mismatch**, **sso_provider_unavailable**:
  the sign-in did not complete; start again, or ask the org's
  administrator.

## Operator API

- **email_invalid**: give the first owner's email address.
- **external_ref_invalid**: an external reference is at most 200
  characters.
- **limit_invalid**: a limit is a number from 0 up, or null for none.
- **status_invalid**, **status_note_invalid**: a status is `active`,
  `read_only` or `suspended`, and its note at most 500 characters.

## CLI sign-in

- **device_name_invalid**: a device name is at most 100 characters.
- **authorization_pending** (400): the sign-in has not been approved in
  the dashboard yet; keep polling.
- **slow_down** (400): polling too often; wait longer between requests.
- **access_denied** (400): the sign-in was denied.
- **expired_token** (400): the code expired; start again.
- **invalid_grant** (400): the device code is unknown or was already
  used.
