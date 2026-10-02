# ADR 0019: The API manages everything a declarative tool needs; admin powers are explicit-only scopes

- Status: proposed
- Date: 2026-10-02

## Context

An external audit of `/v1` (an integrator's view: a self-hosted consumer and
a headless client) found the content pipeline complete and the
administration around it reachable only from the dashboard or the CLI: a
channel that needs reauthorization cannot be reconnected by API, the
weekly slots behind `publish_at: next_slot` cannot be read or set, API keys
cannot be listed, rolled or revoked, posts waiting for approval cannot be
approved, and attempt and audit history cannot be read.

Self-hosters provision declaratively (Terraform/OpenTofu, Ansible, GitOps)
and run headless, and we intend to publish a Terraform provider, Ansible
roles and an MCP server. Each needs the API, not a browser.

Two facts constrain the answer:

- `/v1` never accepts session cookies ([ADR 0005](0005-api-conventions.md)),
  so it has no CSRF exposure. Administration by API therefore means an API
  key with an administrative scope.
- A key created with no scopes holds every scope in `KeyScopes`. Adding a
  permission to that list silently grants it to every such key.

## Decision

1. **Three surfaces, each with one kind of caller.** `/v1` takes API keys;
   the dashboard takes members' sessions; the CLI takes operators with
   database access. There is no session-authenticated API.
2. **Explicit-only scopes.** Some scopes are held only when a key lists them
   by name, never through "no scopes, full access": `posts:approve`,
   `audit:read` and `keys:write` (and `members:write`, if it is ever offered
   to keys). Only a member (dashboard, or CLI) can create a key holding one;
   a key can never grant one.
3. **Keys managing keys** (`keys:write`): list, create, roll and revoke keys
   in the key's own org and mode, only with scopes that are a subset of its
   own, and a brand restriction at least as narrow. Any key may roll itself;
   the old secret keeps working for an overlap (default 24 hours) so
   rotation needs no downtime. The first key always comes from a member.
4. **Approval by API** (`posts:approve`): approve or reject a post waiting
   for review. A key can never approve a post it created; the reviewer
   (member or key) is recorded.
5. **Operations that need no new power get routes now**, guarded by the
   permission they already check: reconnecting and enabling or disabling a
   channel (`channels:write`), a brand's weekly slots on the brand
   (`brands:read`/`brands:write`), a target's attempt history
   (`posts:read`).
6. **Members and org settings stay with members and operators** for now: the
   dashboard, and `araldo members` / `araldo org` in the CLI. A key scope
   comes when a declarative tool needs it.
7. **Declarative clients are first-class.** For every resource:
   - a read returns everything a write set, except secrets, which are
     write-only (a channel shows its non-secret settings);
   - it can be found by its natural key as well as its ID (brands by slug,
     templates by brand and key), and paths take IDs (or a brand's slug)
     because a template key is unique only within a brand;
   - updates change what they name and leave the rest, so a provider can
     update in place instead of replacing.
8. **The contract stays strict both ways.** Unknown query parameters are
   rejected (`parameter_unknown`), as unknown body fields already are. An
   `Idempotency-Key` is kept only for a request that took effect (a 2xx):
   a failed request had no side effects, so a corrected retry with the same
   key runs, as Stripe does.
9. **Optional template fields.** A field the template's schema declares but
   the data omits is null when rendering, so `{{if .field}}` and
   `{{with .field}}` work; printing one unguarded is still an error.

## Alternatives considered

- **A session-authenticated admin API.** A second contract to version, and
  cookies bring CSRF back.
- **Putting admin scopes in `KeyScopes`.** Every existing unscoped key would
  gain them on upgrade.
- **CLI only for administration.** The CLI needs database access, which a
  Terraform run or an Ansible play against a hosted install does not have.

## Consequences

- Before 1.0: explicit-only scopes, strict query parameters, idempotency on
  failure and optional template fields change behavior, so they land now,
  while that is free.
- The rest is additive and lands as needed, contract first.
- A Terraform provider can manage brands (with slots), channels, templates,
  webhook endpoints and API keys; an MCP server can preview, schedule,
  approve and report.
