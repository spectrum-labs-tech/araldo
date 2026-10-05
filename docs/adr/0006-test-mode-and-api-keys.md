# ADR 0006: Every org has a test mode that can never post publicly

- Status: accepted; built; key rolling amended by [ADR 0019](0019-administration-api.md)
- Date: 2026-09-28

## Context

A developer wiring their product to Araldo needs to create posts, see them
"publish", receive webhooks and exercise failures, without anything
appearing on a real account and without waiting for any platform to
approve a developer app. A mistake here is public and embarrassing.

## Decision

1. **Two modes per org, test and live.** The mode comes from the API key
   (or the dashboard's mode switch).
   - **Mode-scoped** (activity): channels, posts and their targets, media,
     events, webhook endpoints, idempotency keys. These rows carry
     `livemode`, and every query filters on it.
   - **Shared** (configuration): brands, templates, schedule slots,
     members. A template built against test mode works unchanged in live
     mode.
2. **Test mode can only use sandbox channels, and live mode can never use
   them.** The database refuses a target whose mode differs from its post's
   or its channel's, and a newsletter delivery whose mode differs from its
   mail account's (composite foreign keys on `livemode`), so this holds even
   where code forgets to check. A sandbox channel for a platform applies that platform's real
   rules ([ADR 0009](0009-platform-adapters.md)) and records the post
   instead of sending it, returning a fake remote ID and permalink.
3. **Failures can be simulated** in test mode with
   `metadata.araldo_simulate`: `rate_limited`, `auth_revoked`, `rejected`,
   `timeout_after_send`, or `slow`. Each produces the same states, events
   and retries the real failure would. Stripe's test card numbers are the
   model.
4. **API keys** look like `ald_live_` or `ald_test_` followed by 32 random
   base62 characters and a 6-character checksum (CRC32, base62), as GitHub's
   tokens do. The distinctive prefix lets secret scanners find leaked keys,
   and the checksum lets them discard false positives without calling us.
   - Only a SHA-256 hash is stored, with the prefix and last four characters
     for display. The full key is shown once, when it is created.
   - A key is either **full access** or **restricted** to a list of scopes
     (`posts:write`, `posts:read`, `templates:write`, …) and optionally to
     one brand.
   - Keys record when they were last used. **Rolling** a key issues a new
     one and keeps the old one working for a chosen overlap (up to 7 days).
   - Creating or rolling a key requires recent re-authentication
     ([ADR 0007](0007-authentication-and-mfa.md)) and is audited.

## Alternatives considered

- **Separate test orgs**, as many APIs do. Templates and brands would have
  to be copied between orgs, and they drift.
- **A per-post `dry_run` flag.** One forgotten flag publishes publicly;
  here the key itself decides, and a test key has no way to reach a real
  account.
- **Everything mode-scoped** (as Stripe does for products). Developers
  would have to recreate templates in live mode by hand.

## Consequences

- Every mode-scoped table has `livemode`, and tests check that a test key
  cannot read or use live rows.
- The sandbox adapter must follow each platform's real rules closely, or
  test mode will pass content that live mode rejects.
- If Araldo is hosted, we can register the `ald_` prefix with GitHub's
  secret scanning partner program.
