# Operating Araldo

## Configuration

Everything comes from environment variables.

| Variable | Required | Meaning |
|---|---|---|
| `ARALDO_DATABASE_URL` | yes | Postgres URL (`DATABASE_URL` also works). The role must own the schema: Araldo runs its own migrations. |
| `ARALDO_MASTER_KEYS` | yes | `id:base64key[,id:base64key…]`, primary first ([ADR 0008](adr/0008-encryption.md)). Or `ARALDO_MASTER_KEYS_FILE`. |
| `ARALDO_BASE_URL` | yes | The public URL, e.g. `https://araldo.example.com`. |
| `ARALDO_LISTEN` | | HTTP address, default `:8080`. |
| `ARALDO_AUTO_MIGRATE` | | Migrate at startup, default `true`. The Helm chart sets it to `false` and migrates in a hook instead (see Kubernetes). |
| `ARALDO_CLIENT_IP_HEADER` | | Trusted proxy header with the client IP (e.g. `CF-Connecting-IP`), for sign-in rate limits. |
| `ARALDO_ALLOW_PRIVATE_NETWORKS` | | Let webhooks and adapters reach private addresses. Off by default. |
| `ARALDO_INSECURE_COOKIES` | | Plain-HTTP development only. |
| `ARALDO_LOG_LEVEL` | | `debug`, `info` (default), `warn`, `error`. |
| `ARALDO_S3_BUCKET` | | Store new media in this S3-compatible bucket instead of Postgres (see Media). |
| `ARALDO_S3_ENDPOINT` | with a bucket | The service URL, e.g. `https://<account>.r2.cloudflarestorage.com`. |
| `ARALDO_S3_ACCESS_KEY_ID`, `ARALDO_S3_SECRET_ACCESS_KEY` | with a bucket | Credentials that can put, get and delete objects. |
| `ARALDO_S3_REGION` | | Default `auto` (R2); AWS needs the bucket's region. |
| `ARALDO_S3_PREFIX` | | Key prefix, default `media/`. |

## First run

```sh
araldo keys generate --id k1          # keep this safe, outside the database
export ARALDO_MASTER_KEYS=k1:…
araldo bootstrap --email you@example.com --org "Your org" --brand "Your product"
araldo server & araldo worker         # or: araldo all
```

`bootstrap` prints a generated password once. Sign in, then turn on
two-factor authentication under **Your account**.

## Keys

- **Back up the master keys separately from the database.** Without them,
  connected channels must be reconnected (posts and history survive).
- Rotate: generate a new key, put it first in `ARALDO_MASTER_KEYS` (keep the
  old one after it), restart, run `araldo keys rotate`, then remove the old
  key.

## Users

- `araldo users create --email …` and `araldo users reset-password --email …`
  print a generated password (or read one with `--password-stdin`).

## API keys for other services

`araldo apikeys create` makes a key without the dashboard, for provisioning
a service's key straight into its secret store. It prints only the key on
stdout, so pipe it rather than copying it:

```bash
araldo apikeys create --email you@example.com --brand your-product \
  --name "your-service staging" --scopes posts:write,templates:read,templates:write \
  | your-secret-store put …
```

The key acts for the member named by `--email` and needs their permission to
manage keys. Add `--live` for a live key and `--expires 8760h` to expire it.

### Administration by key

A key with no scopes holds every integration scope. Three administrative
scopes are held only when listed by name, and only a member (here, or the
dashboard) can grant them ([ADR 0019](adr/0019-administration-api.md)):

| Scope | Lets the key |
|---|---|
| `keys:write` | List, create, roll and revoke keys in its mode, with no more access than its own (`/v1/api_keys`). For a Terraform provider or a secrets rotator. |
| `posts:approve` | Approve or reject posts waiting for review (`/v1/posts/{id}/approve`, `/reject`), never one it created. For approving from Slack or your own tools. |
| `audit:read` | Read the audit log (`/v1/audit_events`). For exporting to a SIEM. |

```bash
araldo apikeys create --email you@example.com --name "terraform" \
  --scopes brands:read,brands:write,channels:read,channels:write,templates:read,templates:write,webhooks:read,webhooks:write,keys:write
```

Listing any scope ends "full access", so a key that needs integration work
and administration lists both.

### Rotating keys

Roll a key in the dashboard, or have it roll itself with
`POST /v1/api_keys/self/roll`: the new secret has the same scopes, and the
old one keeps working for 24 hours (`overlap_hours`, up to 168) so the new
one can be deployed without downtime.

## Connecting platforms

Most platforms connect with credentials pasted into **Channels → Connect a
channel** in live mode; the form says where each comes from. Some connect
with a sign-in through a **developer app** you register with the platform
([ADR 0021](adr/0021-oauth-connections.md)): add it under **Channels →
Developer apps**, which shows the redirect URI to give the platform, then
connect channels through it. Tokens are stored encrypted and renewed before
they expire (the `channels.refresh` task); a channel whose token can no
longer be renewed shows *needs reauth* and reconnects the same way.

| Platform | How it connects |
|---|---|
| Bluesky | Handle and an app password. |
| Mastodon | Server and an access token (write:statuses, write:media, read:accounts). |
| Gab | An access token (write:statuses, write:media, read:accounts). Gab is one service, so there is no server to give. |
| X | A sign-in through your X app, below; or the app's API key and secret and the account's access token and secret, pasted (read and write). |
| LinkedIn | A sign-in through your LinkedIn app, below; or a member access token from its token tools (openid, profile, w_member_social), pasted. Tokens last 60 days. |
| Threads | A sign-in through your Threads app, below. |
| Facebook Pages, Instagram | A sign-in through your Meta app, below; you choose which Pages, or which Instagram accounts linked to them. |
| Discord, Telegram | A webhook URL; a bot token and chat. |

**X.** At developer.x.com, in your app's *User authentication settings*,
turn on OAuth 2.0 as a *Web App* (a confidential client) with read and
write permission, and add Araldo's redirect URI
(`{ARALDO_BASE_URL}/connect/x/callback`) as a callback URI. Add the app's
OAuth 2.0 client ID and secret (not the API key) under Developer apps,
then connect. The sign-in asks for `tweet.read`, `tweet.write`,
`users.read`, `media.write` and `offline.access`; tokens last two hours and
are renewed automatically, each renewal replacing the refresh token. What
an app may post and read depends on its X API access tier.

**LinkedIn.** At linkedin.com/developers, add the products *Sign In with
LinkedIn using OpenID Connect* and *Share on LinkedIn* to your app, and
add Araldo's redirect URI (`{ARALDO_BASE_URL}/connect/linkedin/callback`)
under Auth → Authorized redirect URLs. Add the app's client ID and secret
under Developer apps, then connect. Tokens last 60 days. LinkedIn gives
refresh tokens only to apps it has approved for them; without one, the
channel says a week ahead when to sign in again, and needs it once the
token expires.

**Threads.** At developers.facebook.com, create an app with the *Access the
Threads API* use case; add the permissions `threads_basic`,
`threads_content_publish` and `threads_manage_insights`; under its
settings add Araldo's redirect URI (`{ARALDO_BASE_URL}/connect/threads/callback`)
to the redirect callback URLs; and, while the app is in development, add
the Threads accounts you will connect as testers (they accept in Threads
→ Settings → Website permissions). Add the app's Threads app ID and secret
under Developer apps, then connect. Threads fetches images from a link to
the install, so posts with images need `/v1` reachable from the internet;
the engagement it reports includes views.

**Facebook Pages and Instagram.** At developers.facebook.com, create a
Business app with Facebook Login for Business; request `pages_show_list`,
`pages_manage_posts`, `pages_read_engagement` (Pages) and
`instagram_basic`, `instagram_content_publish`, `business_management`
(Instagram, which must be a professional account linked to a Page); add
Araldo's redirect URIs (`{ARALDO_BASE_URL}/connect/facebook/callback` and
`…/connect/instagram/callback`) to the valid OAuth redirect URIs. While the
app is in development it works for people with a role on it; publishing
for others needs Meta's app review. The same app can serve both: add it
under Developer apps once as Facebook and once as Instagram. Page tokens
do not expire. Facebook images are uploaded; Instagram fetches them from a
link to the install, like Threads.

## AI assistants (MCP)

`araldo mcp` gives an AI assistant Araldo's tools over the Model Context
Protocol ([ADR 0020](adr/0020-mcp.md)). It talks to an Araldo API with a
key, so it needs no database, and the key decides what the assistant may
do: start with a test key (its posts reach only sandbox channels), then a
live key limited to the brand it should post for.

| Tool | Does |
|---|---|
| `list_platforms`, `list_brands`, `list_channels`, `list_templates`, `get_template` | Read what there is (read-only). |
| `upload_media_from_url` | Fetch an image for posts to attach. |
| `preview_post` | Render a post per channel and list every rule it breaks (read-only). |
| `create_post` | Schedule it (with an idempotency key, so a retry does not post twice). |
| `list_posts`, `get_post` | Follow up: status, links, errors, engagement (read-only). |
| `cancel_post` | Stop what has not published yet (marked destructive). |
| `reschedule_post` | Move a post to another time or slot, or swap it with another. |
| `engagement_summary` | What did best, by post, channel or template (read-only). |
| `ads_summary` | Ad spend and results, by brand, account, campaign or day (read-only). |

Claude Code:

```bash
claude mcp add araldo --env ARALDO_URL=https://araldo.example.com \
  --env ARALDO_API_KEY=ald_test_… -- araldo mcp
```

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "araldo": {
      "command": "araldo",
      "args": ["mcp"],
      "env": { "ARALDO_URL": "https://araldo.example.com", "ARALDO_API_KEY": "ald_test_…" }
    }
  }
}
```

**Over HTTP**, with nothing to install: the server answers MCP at
`POST /v1/mcp` with the same tools, as the key sent with it (scopes, brand
limit and rate limit apply). In Claude Code:

```bash
claude mcp add --transport http araldo https://araldo.example.com/v1/mcp   --header "Authorization: Bearer ald_test_…"
```

Any MCP client that connects to a URL with a header works the same way.
The endpoint is stateless (no session, no server-initiated messages) and
refuses requests a browser makes from another site.

The key needs `brands:read`, `channels:read` and `posts:read`/`posts:write`
(and `templates:read` for templates, `ads:read` for `ads_summary`); a key
with no scopes listed has them all. It does not need, and should not have,
an administrative scope.

## Members and org settings

Members, their roles and the org's settings are for people, not API keys:
the dashboard, or these commands, which act as the member named by `--as`
and need that member's role to allow the change:

```bash
araldo members list   --as you@example.com
araldo members add    --as you@example.com --email them@example.com --role editor
araldo members role   --as you@example.com --email them@example.com --role admin
araldo members remove --as you@example.com --email them@example.com
araldo org update     --as you@example.com --name "Spectrum Labs" --require-mfa true
```

`members add` prints a temporary password for someone without an account
(or reads one with `--password-stdin`).

## Media

Images attached to posts ([ADR 0017](adr/0017-media.md)) are stored in
Postgres by default, so server and worker share them with nothing more to
set up, and database backups include them. An install posting many large
images should use S3-compatible storage instead (the `ARALDO_S3_*`
variables): new files go there, files stored earlier stay readable where
they are, and files in a bucket need their own backups. Media no post uses
is deleted after a day (the `media.prune` task); media a post uses is kept
as long as the post. With the Helm chart, put the S3 keys in the
`existingSecret` and the rest in `extraEnv`; the server and the worker
must both have them, since one stores files and the other reads them.

Mastodon and Gab channels need an access token with the `write:media` scope
to post images; one made before images were supported must be replaced.

**Gab.** Gab Social is a Mastodon fork, so a Gab channel works like a
Mastodon one without the server field, and posts can run to 3000 characters.
Two things differ in practice. Gab makes no promise about `Idempotency-Key`,
so Araldo treats it as non-idempotent: an attempt whose outcome is unknown
waits for a person (Posts -> *needs attention*) instead of being retried,
where a Mastodon target would retry itself. And Gab documents no image size
limit, so Araldo checks only the image's type and leaves the size to Gab --
an image it refuses comes back as a publishing failure rather than a rule
violation at preview.

## Engagement

The `engagement.collect` task reads each published post's likes, reposts,
replies and quotes 1 hour, 6 hours, 1, 3, 7 and 30 days after publishing
([ADR 0018](adr/0018-engagement.md)): Bluesky through its public AppView
(no sign-in), Mastodon and Gab with the channel's token. Gab reports no
quote count, which stays zero. Discord and Telegram do not report
engagement. Posts published before an upgrade to a version with
engagement are read once soon after it, then on the schedule. A failed
reading is retried later and never affects publishing; see the target's
`engagement.state` and the task on Organization → Background tasks.

## Ads

Araldo reads ad accounts' spend and results so they sit next to organic
engagement, across brands ([ADR 0023](adr/0023-paid-promotion.md)). It
spends nothing: campaigns are made in each network's own tools, and Araldo
reads every campaign in a connected account. Connect accounts on the
dashboard's **Ads** page or with `POST /v1/ad_accounts`; both need the
explicit `ads:write` scope (admins and owners). Reading needs `ads:read`.

The `ads.collect` task reads an account soon after it connects (the last
30 days), then daily, each time re-reading the last 7 days, since networks
revise a day's numbers as late conversions are attributed. Amounts are in
the account's currency, in its minor unit. A network that refuses the
credentials marks the account *needs reauth*; connect it again.

Test mode has a sandbox network with invented numbers.

**Reddit.** At reddit.com/prefs/apps, create a *web app* with Araldo's
redirect URI (`{ARALDO_BASE_URL}/connect/reddit_ads/callback`), and ask
Reddit for Ads API access for it. Add its client ID and secret under
Channels → Developer apps as *Reddit Ads*, then on the Ads page sign in
and choose the ad accounts to read. Araldo asks only for `adsread`, with
a permanent refresh token it swaps for an hour-long access token on each
read. Spend arrives in millionths of the account's currency and is stored
in its minor unit. Results stay 0: they count conversions, which Reddit
measures with its pixel. To count signups, tag ad links with UTM parameters
(`utm_medium=paid`) and read them in your own analytics: Araldo never asks
for a network's tracking pixel.

## Health

- `GET /healthz`: the process is up.
- `GET /readyz`: the database answers and the schema is current.
- The dashboard's **Organization → Background tasks** shows every periodic
  task, its last success and failures.

## Metrics

`araldo server`, `araldo worker` and `araldo all` export OpenTelemetry
metrics, configured only through the standard `OTEL_*` variables
([ADR 0014](adr/0014-telemetry.md)). Nothing is exported until one is set.

| Variable | Meaning |
|---|---|
| `OTEL_METRICS_EXPORTER` | `prometheus` serves a scrape endpoint; `otlp` pushes to a collector; `console`; `none`. |
| `OTEL_EXPORTER_PROMETHEUS_HOST`, `OTEL_EXPORTER_PROMETHEUS_PORT` | Where `/metrics` listens, default `localhost:9464`. Use `0.0.0.0` for scrapes from other hosts. It is a separate listener: the public port never serves metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` (or `…_METRICS_ENDPOINT`), `OTEL_EXPORTER_OTLP_PROTOCOL` | The collector for `otlp`; setting an endpoint alone also turns OTLP export on. |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | Resource attributes (default `service.name=araldo`). |
| `OTEL_SDK_DISABLED=true` | Turns everything off. |

### What is measured

Names as Prometheus shows them. Labels are bounded: never an org, brand,
user, channel, post or target ID, never post text, and errors only by kind,
never by message. `mode` is `live` or `test`.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `araldo_post_targets` | gauge | `status`, `mode` | Post targets in each status (`queued`, `publishing`, `held`, `needs_attention`, `failed`, `published`, `canceled`), zeros included. |
| `araldo_post_targets_due` | gauge | `mode` | Queued targets the publisher could claim now: due, on an active channel not held by a rate limit. |
| `araldo_post_targets_oldest_due_age_seconds` | gauge | `mode` | How long the oldest of those has been due; 0 when none is. |
| `araldo_task_last_success_timestamp_seconds` | gauge | `task` | Unix time of each background task's last successful run. |
| `araldo_task_consecutive_failures` | gauge | `task` | Failures of each task since its last success. |
| `araldo_publish_attempts_total` | counter | `provider`, `mode`, `outcome`, `error_kind` | Publish attempts. `outcome` is what the publisher did: `published`, `retry`, `rate_limited`, `needs_attention`, `failed`. `error_kind` is the platform's classification: `none`, `rate_limited`, `auth_revoked`, `transient`, `uncertain`, `rejected`, `unknown`. |
| `araldo_webhook_delivery_attempts_total` | counter | `mode`, `outcome` | Webhook delivery attempts: `succeeded`, `retry`, `failed` (gave up after three days). |
| `araldo_task_failures_total` | counter | `task` | Failed background task runs (errors, panics, timeouts). |

The gauges are read from the database when metrics are collected (one
grouped count of targets and the task table, at most every 10 seconds per
process) and cover every org. Every process reports the same values, so
aggregate them with `max`, not `sum`. The counters count what each process
did: publishing and deliveries happen in workers, task failures in whichever
worker holds the task's lease, so `sum` them.

### Helm chart

All off by default:

| Value | Effect |
|---|---|
| `metrics.enabled` | Sets `OTEL_METRICS_EXPORTER=prometheus` and the port (`metrics.port`, default 9464) on the server and worker, and declares a `metrics` container port. The Service does not expose it. |
| `metrics.podMonitor.enabled` | A `monitoring.coreos.com/v1` PodMonitor scraping both roles (`interval`, `scrapeTimeout`, `labels` for your Prometheus's selector). Needs `metrics.enabled`. |
| `metrics.prometheusRule.enabled` | A `monitoring.coreos.com/v1` PrometheusRule with the alerts below. `labels` go on the object, `alertLabels` on every alert; `dashboardURL` (default `config.baseURL`) is where runbook links point; `selector` overrides the PromQL matchers that pick this release's series (default: the release namespace, and the PodMonitor's job when it is on). |

Each alert under `metrics.prometheusRule.alerts` has `enabled`, `for`,
`severity` and its thresholds:

| Alert | Fires when (defaults) | Runbook link |
|---|---|---|
| `AraldoPostNeedsAttention` (warning) | `max(araldo_post_targets{mode="live", status="needs_attention"}) > 0` for 5m. | Home, where live mode counts targets needing attention. Open each post, check the account, then **Retry** or **Mark published** ([ADR 0011](adr/0011-publishing.md)). |
| `AraldoPublishFailing` (warning) | Per provider, over `window` (30m), at least `minFailures` (3) live attempts ended in `retry`, `failed` or `needs_attention`, and they are at least `ratio` (0.5) of that provider's live attempts; for 15m. Rate limits do not count. | Posts: each post's attempt history shows the platform's error; Channels shows accounts to reconnect. |
| `AraldoPublishingStalled` (critical) | The oldest claimable live target has been due over `dueSeconds` (900), or `publish.reclaim` last succeeded over `taskStaleSeconds` (600) ago; for 5m. The worker is down, stuck or cannot reach the database. | Organization → Background tasks. |
| `AraldoBackgroundTaskFailing` (warning) | `max by (task) (araldo_task_consecutive_failures) >= consecutiveFailures` (3) for 5m. | Organization → Background tasks, with the last error. |

The alerts cannot see a deployment with no running pods at all; alert on
the scrape targets themselves (`up`) for that.

## Kubernetes

The chart is in `deploy/helm/araldo` and published to
`oci://ghcr.io/spectrum-labs-tech/charts/araldo`: `0.0.0-main` follows the
main branch (its appVersion pins the exact image), and `X.Y.Z` follows
release tags. It needs `existingSecret` (a Secret with `ARALDO_DATABASE_URL`
and `ARALDO_MASTER_KEYS`) and `config.baseURL`.

**Migrations run before the rollout.** A `pre-install`/`pre-upgrade` hook Job
runs `araldo migrate`; only when it succeeds does Helm update the Deployments.
If it fails, the upgrade stops, the running pods keep serving the previous
version on the previous schema, the release is marked `failed`, and the Job is
kept so `kubectl logs job/<release>-migrate` shows why. The pods do not
migrate, and `/readyz` stays unready until the schema is current. Write
migrations that the previous version can still run against (add before you
remove), since it keeps serving until the new pods are ready. Rendering the
chart without hooks (`helm template | kubectl apply`)? Set
`migrations.job=false` and the pods migrate at startup instead.
