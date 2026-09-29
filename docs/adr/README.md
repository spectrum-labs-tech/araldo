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
