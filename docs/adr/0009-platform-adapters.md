# ADR 0009: Platforms are adapters; developer app credentials live in the database

- Status: proposed
- Date: 2026-09-28

## Context

Every platform differs in how you connect (OAuth 2.0 with PKCE, app
passwords, per-server app registration on Mastodon), what a post can hold,
how length is counted, and how failures look. Most platforms also require
a registered **developer app**, some behind business verification and app
review (Meta, TikTok, LinkedIn). Quotas and enforcement often apply to the
developer app as a whole, so every account connected through one app
shares its limits and its standing.

The usual core is right (one adapter interface, a registry), but reading
credentials from environment variables means exactly one developer app per
platform per install.

## Decision

1. **Terms:**
   - **Provider:** a platform (`bluesky`, `mastodon`, `x`, `facebook`,
     `instagram`, `threads`, `linkedin`, `youtube`, `tiktok`, `pinterest`,
     `reddit`, `discord`, `telegram`, plus `sandbox`).
   - **Provider app:** a developer app registered with a provider (client
     ID, encrypted secret, redirect URI).
   - **Channel:** one connected account, page or profile, under a brand.
2. **Provider apps are rows in the database**, not environment variables.
   An app is either install-wide (the operator's default) or owned by an
   org, and a brand chooses which app its channels connect through. This
   lets self-hosters register their own apps, and lets one install keep
   different kinds of content on separate apps, so a policy strike or an
   exhausted quota on one does not stop the others. Mastodon apps are
   registered automatically, one per server, on first connection.
3. **Adapters** implement one small interface, plus optional ones:

   ```go
   type Adapter interface {
       Provider() Provider
       Rules() Rules                                  // limits, as data
       Publish(ctx context.Context, c Credentials, p Payload) (Result, error)
   }
   type Connector interface { /* start and finish the connect flow */ }
   type Refresher interface { Refresh(ctx context.Context, c Credentials) (Credentials, error) }
   type Finder    interface { FindRecent(ctx context.Context, c Credentials, since time.Time) ([]RemotePost, error) }
   ```

   `Finder` lets the publisher check whether an uncertain attempt actually
   posted ([ADR 0011](0011-publishing.md)).
4. **Rules are data:** maximum length and how it is counted (X's weighted
   count with fixed-length links, graphemes on Bluesky, the server's own
   limit on Mastodon), media required, allowed, counted and sized, aspect
   ratios, threads, and link handling. Template validation, preview and the
   sandbox adapter all use the same rules. Every limit in code cites the
   platform documentation it came from.
5. **Errors are classified**, because the publisher acts on the class:
   - `RateLimited` (with retry-after): wait, then retry;
   - `AuthRevoked`: the channel needs reconnecting, and the org is
     notified;
   - `Rejected`: permanent, such as content policy or invalid media;
   - `Transient`: retry with backoff;
   - `Uncertain`: the request may have been received, for example a
     timeout after sending.
6. **Channel health:** tokens are refreshed before they expire (an `opsched`
   task), and a channel whose refresh fails becomes `needs_reauth` and
   emits `channel.needs_reauth`.
7. **Order of work:**
   1. `sandbox` (test mode, [ADR 0006](0006-test-mode-and-api-keys.md));
   2. `bluesky`, `mastodon`, `discord` (webhook), `telegram` (bot): no
      approval needed;
   3. `x`, `facebook` pages, `instagram`, `threads`;
   4. `linkedin`, `youtube`, `tiktok`, `pinterest`, `reddit`.
8. **Media** lives in S3-compatible storage (local disk in development).
   Version 1 validates media against the rules and rejects what does not
   fit; resizing and transcoding come later.

## Alternatives considered

- **Environment variables per platform.** One app per
  install, no self-service, and a restart for every change.
- **A generic "HTTP recipe" adapter** configured in YAML. Platforms differ
  in too many ways (uploads, threads, refresh) for configuration to stay
  simpler than code.

## Consequences

- The connect flow needs a public callback URL per install
  (`/connect/<provider>/callback`).
- Platform approvals (Meta business verification, TikTok's audit, LinkedIn's
  Community Management API) are operator tasks; the docs will walk through
  each one.
- Adding a platform means: an adapter, its rules with sources, a sandbox
  test run against those rules, and tests against recorded HTTP exchanges.
  No test calls a real platform.
