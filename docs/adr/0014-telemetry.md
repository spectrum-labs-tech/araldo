# ADR 0014: OpenTelemetry and slog, with no secrets or unpublished content

- Status: accepted; slog and metrics built; traces, logs through OpenTelemetry and publish lateness not yet
- Date: 2026-09-28

## Context

Operators need to see whether posts go out on time, why a channel failed,
and whether webhooks are backing up. Telemetry is also a classic leak: a
token in a log line, an unannounced product launch in a trace attribute.

## Decision

1. **OpenTelemetry** for traces, metrics and logs, configured only through
   the standard `OTEL_*` variables (`autoexport`, so Prometheus scraping
   works with `OTEL_METRICS_EXPORTER=prometheus`).
2. **Logging is `log/slog`**, JSON to stderr, with `…Context` methods inside
   requests so records carry trace IDs. The linter rejects the `log`
   package and `fmt.Print*`.
3. **Never recorded anywhere in telemetry:**
   - tokens, keys, secrets or `Authorization` headers;
   - request or response bodies;
   - post text, template bodies or media URLs: content is confidential
     until it is published;
   - query strings or SQL values.

   Errors are recorded by kind (`rate_limited`, `auth_revoked`), not by
   message, because platform error messages echo content.
4. **Metric attributes are bounded:** provider, status, task name. Never an
   org, brand, channel or post ID, which would give one time series per
   tenant.
5. **Key metrics:**
   - targets published, failed and uncertain, by provider;
   - how late publishing runs (actual time minus scheduled time);
   - queue depth, and the age of the oldest due target;
   - webhook delivery success and backlog;
   - channels needing reauthorization;
   - token refresh failures;
   - time since the last successful backup.

   Tests assert that none of the forbidden values reach the exporters (the
   in-memory exporters from `tracetest` and `sdkmetric`).
6. **Health endpoints:** `/healthz` (the process is alive) and `/readyz`
   (database reachable, migrations current, keyring loaded).

## Consequences

- Per-tenant debugging uses the request log and event history in the
  product, not metrics.
- Every new piece of instrumentation needs a test that checks it leaks
  nothing.
