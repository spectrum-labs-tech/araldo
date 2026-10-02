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

Mastodon channels need an access token with the `write:media` scope to
post images; one made before images were supported must be replaced.

## Engagement

The `engagement.collect` task reads each published post's likes, reposts,
replies and quotes 1 hour, 6 hours, 1, 3, 7 and 30 days after publishing
([ADR 0018](adr/0018-engagement.md)): Bluesky through its public AppView
(no sign-in), Mastodon with the channel's token. Discord and Telegram do not
report engagement. Posts published before an upgrade to a version with
engagement are read once soon after it, then on the schedule. A failed
reading is retried later and never affects publishing; see the target's
`engagement.state` and the task on Organization → Background tasks.

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
