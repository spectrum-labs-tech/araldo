# Architecture decision records

Significant decisions are recorded here so newcomers can learn *why*, not just
*what*. To propose one, copy [template.md](template.md) to the next number and
open a pull request.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-scope.md) | Araldo is a distribution API for developers; callers bring the content | accepted; ads narrowed by ADR 0023 |
| [0002](0002-layout-and-storage.md) | One module, one binary, Postgres only, a fixed layout | accepted; built |
| [0003](0003-license.md) | AGPL-3.0-or-later for the server; permissive licenses for clients | accepted; in effect (the DCO sign-off is not checked in CI) |
| [0004](0004-tenancy-roles-approvals.md) | Orgs are the tenant boundary; brands group channels; four roles and optional approvals | accepted; built; brand-limited members remain deferred |
| [0005](0005-api-conventions.md) | A contract-first API with Stripe-style conventions | accepted; built, except SDKs and the dashboard's request log; amended by ADR 0019 and ADR 0028 |
| [0006](0006-test-mode-and-api-keys.md) | Every org has a test mode that can never post publicly | accepted; built; key rolling amended by ADR 0019 |
| [0007](0007-authentication-and-mfa.md) | Passwords, passkeys and TOTP built in; MFA can be required per org | accepted; passwords, TOTP, recovery codes, required MFA and passkeys built; email flows and the breached-password check not yet |
| [0008](0008-encryption.md) | Secrets are envelope-encrypted per org; the master keys never touch the database | accepted; built (deleting an org crypto-shreds it), except `keys rotate-org` and `keys export` |
| [0009](0009-platform-adapters.md) | Platforms are adapters; developer app credentials live in the database | accepted; built for every platform but Reddit; Mastodon app registration not yet; install-wide apps in ADR 0030 |
| [0010](0010-templates.md) | Templates are versioned Go text/templates with a JSON Schema and per-platform bodies | accepted; built, except the per-version `media` field and warnings on save |
| [0011](0011-publishing.md) | Publishing is an outbox with leases, and it never double-posts silently | accepted; built, except `Finder` reconciliation and editing a queued target; slots changed by ADR 0022 |
| [0012](0012-events-and-webhooks.md) | Events are written in the same transaction; webhooks are signed and retried | accepted; built, except emailing admins about a disabled endpoint (Araldo sends no email yet) |
| [0013](0013-backups.md) | Built-in encrypted backups that the server itself cannot read | withdrawn: backups are the operator's |
| [0014](0014-telemetry.md) | OpenTelemetry and slog, with no secrets or unpublished content | accepted; slog and metrics built; traces, logs through OpenTelemetry and publish lateness not yet |
| [0015](0015-dashboard.md) | A server-rendered dashboard embedded in the binary | accepted; built, except the calendar and the request log |
| [0016](0016-link-tagging.md) | Tag links with UTM parameters at render time; leave clicks to web analytics | accepted; built |
| [0017](0017-media.md) | Images are uploaded once, checked against every platform's rules, and stored in Postgres unless S3 is configured | accepted; built; extended by ADR 0027 |
| [0018](0018-engagement.md) | Read each published post's engagement on a fixed schedule and keep every reading | accepted; built; readers for X, LinkedIn, Pinterest and TikTok not yet |
| [0019](0019-administration-api.md) | The API manages everything a declarative tool needs; admin powers are explicit-only scopes | accepted; built; decision 1 and the CLI half of decision 6 superseded by ADR 0028 |
| [0020](0020-mcp.md) | An MCP server in the binary, as a client of the public API | accepted; built (sign-in for hosted assistants later) |
| [0021](0021-oauth-connections.md) | Channels connect with OAuth through an org's developer apps; images reach platforms by signed links | accepted; built (install-wide apps in ADR 0030) |
| [0022](0022-slots-at-approval.md) | A post takes its publishing slot when it is approved, and posts can be moved | accepted; built |
| [0023](0023-paid-promotion.md) | Paid promotion on any network, with spend that cannot exceed a cap | accepted; phase 1 (reporting) built; promotions not yet |
| [0024](0024-newsletters.md) | Newsletters are designed and scheduled in Araldo and sent by the email provider, which owns the list | accepted; phase 1 built |
| [0025](0025-web-analytics.md) | Web analytics are provider adapters that report visits and signups by Araldo's own link tags | accepted; built for Plausible and GA4 |
| [0026](0026-reports.md) | A brand's report is computed on demand from what Araldo already reads, one period at a time | accepted; phase 1 and share links built; monthly email not yet |
| [0027](0027-video-and-resizing.md) | Images too big for a platform are resized for it, and video is media stored in object storage | accepted; built |
| [0028](0028-cli-as-api-client.md) | The CLI is an API client, modeled on `gh`; only server administration touches the database | accepted; built (device sign-in and user tokens, a test and a live credential per server, `/v1/members` and `/v1/org`, `araldo admin`); `araldo listen`; `auth switch` not yet |
| [0029](0029-backward-compatible-migrations.md) | Migrations work with the release still running, and a test enforces it | accepted; built |
| [0030](0030-install-wide-apps.md) | Install-wide developer apps, managed by the operator, offered to every org beside its own | accepted; built |
| [0031](0031-operator-api.md) | An operator API, org limits and status, and links out for sign-up and billing | accepted; built |
| [0032](0032-request-log.md) | A request log of every authenticated API request, for the org's developers | accepted; built |
| [0033](0033-single-sign-on.md) | OIDC single sign-on by DNS-verified domain, joining automatically, optionally required | accepted; built |
| [0034](0034-notifications-and-email.md) | Notifications in the dashboard and by email, with SMTP set by the operator and self-service password reset | accepted; built |
