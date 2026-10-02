# Roadmap

## Done (first release, September 2026)

- Tenancy: orgs, brands, members with four roles, optional approvals, audit
  log.
- Sign-in: argon2id passwords, TOTP with QR enrollment, recovery codes,
  sessions, re-authentication for sensitive actions, org-wide MFA
  requirement.
- API: `/v1` with API keys (test and live, scoped, brand-limited),
  idempotency keys, cursor pagination, problem details, rate limits; the
  OpenAPI contract with a test that keeps it in step with the routes.
- Templates with JSON Schema, per-platform bodies and fit modes; previews
  with per-platform violations.
- Publishing: slots, deadlines, rate-limit holds, re-auth detection, never
  silently double-posting; sandbox (with failure simulation), Bluesky,
  Mastodon, Discord and Telegram.
- Events and signed webhooks with retries, a delivery log and resend.
- UTM tagging of links to a brand's own sites, so web analytics can credit
  each network, template and post; Bluesky posts show short links
  ([ADR 0016](adr/0016-link-tagging.md)).
- Dashboard for all of the above; one binary; Helm chart; CI publishing to
  GHCR.
- Metrics through OpenTelemetry (Prometheus or OTLP), with the Helm chart's
  optional PodMonitor and alerts ([operations](operations.md#metrics)).
- Images on posts: uploads or URLs, checked against each platform's limits,
  stored in Postgres or S3-compatible storage
  ([ADR 0017](adr/0017-media.md)).

## Next

1. **X, Facebook Pages, Instagram, Threads** (ported from ar15.build's
   `pkg/social`), with provider apps stored in the database
   ([ADR 0009](adr/0009-platform-adapters.md)) and OAuth connect flows.
2. **More media**: video, and resizing images to fit each platform.
3. **ar15.build as the first tenant**: its daily featured build and brand
   posts sent through the API.
4. **Developer tooling**: `araldo listen` (webhooks to localhost), the MCP
   server, TypeScript and Go SDKs generated from the contract, a request
   log in the dashboard.
5. **Passkeys** (WebAuthn) and OIDC single sign-on.
6. **Backups** built in ([ADR 0013](adr/0013-backups.md)), and the rest of
   OpenTelemetry: traces, logs, publish lateness, channels needing
   reauthorization ([ADR 0014](adr/0014-telemetry.md)).
7. **LinkedIn, YouTube, TikTok, Pinterest, Reddit.**
8. **Newsletters**: lists, double opt-in, one-click unsubscribe, SES /
   Brevo / Resend / SMTP, bounces and complaints, digests from templates.
