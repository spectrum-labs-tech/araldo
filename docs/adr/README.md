# Architecture decision records

Significant decisions are recorded here so newcomers can learn *why*, not just
*what*. To propose one, copy [template.md](template.md) to the next number and
open a pull request.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-scope.md) | Araldo is a distribution API for developers; callers bring the content | proposed |
| [0002](0002-layout-and-storage.md) | One module, one binary, Postgres only, a fixed layout | proposed |
| [0003](0003-license.md) | AGPL-3.0-or-later for the server; permissive licenses for clients | proposed |
| [0004](0004-tenancy-roles-approvals.md) | Orgs are the tenant boundary; brands group channels; four roles and optional approvals | proposed |
| [0005](0005-api-conventions.md) | A contract-first API with Stripe-style conventions | proposed |
| [0006](0006-test-mode-and-api-keys.md) | Every org has a test mode that can never post publicly | proposed |
| [0007](0007-authentication-and-mfa.md) | Passwords, passkeys and TOTP built in; MFA can be required per org | proposed |
| [0008](0008-encryption.md) | Secrets are envelope-encrypted per org; the master keys never touch the database | proposed |
| [0009](0009-platform-adapters.md) | Platforms are adapters; developer app credentials live in the database | proposed |
| [0010](0010-templates.md) | Templates are versioned Go text/templates with a JSON Schema and per-platform bodies | proposed |
| [0011](0011-publishing.md) | Publishing is an outbox with leases, and it never double-posts silently | proposed |
| [0012](0012-events-and-webhooks.md) | Events are written in the same transaction; webhooks are signed and retried | proposed |
| [0013](0013-backups.md) | Built-in encrypted backups that the server itself cannot read | proposed |
| [0014](0014-telemetry.md) | OpenTelemetry and slog, with no secrets or unpublished content | proposed |
| [0015](0015-dashboard.md) | A server-rendered dashboard embedded in the binary | proposed |
| [0016](0016-link-tagging.md) | Tag links with UTM parameters at render time; leave clicks to web analytics | proposed |
| [0017](0017-media.md) | Images are uploaded once, checked against every platform's rules, and stored in Postgres unless S3 is configured | proposed |
| [0018](0018-engagement.md) | Read each published post's engagement on a fixed schedule and keep every reading | proposed |
| [0019](0019-administration-api.md) | The API manages everything a declarative tool needs; admin powers are explicit-only scopes | proposed |
| [0020](0020-mcp.md) | An MCP server in the binary, as a client of the public API | proposed |
| [0021](0021-oauth-connections.md) | Channels connect with OAuth through an org's developer apps; images reach platforms by signed links | proposed |
| [0022](0022-slots-at-approval.md) | A post takes its publishing slot when it is approved, and posts can be moved | proposed |
| [0023](0023-paid-promotion.md) | Paid promotion on any network, with spend that cannot exceed a cap | proposed |
| [0024](0024-newsletters.md) | Newsletters are designed and scheduled in Araldo and sent by the email provider, which owns the list | accepted |
| [0025](0025-web-analytics.md) | Web analytics are provider adapters that report visits and signups by Araldo's own link tags | proposed |
| [0026](0026-reports.md) | A brand's report is computed on demand from what Araldo already reads, one period at a time | proposed |
| [0027](0027-video-and-resizing.md) | Images too big for a platform are resized for it, and video is media stored in object storage | accepted |
| [0028](0028-cli-as-api-client.md) | The CLI is an API client, modeled on `gh`; only server administration touches the database | proposed |
