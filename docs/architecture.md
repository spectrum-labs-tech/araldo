# Architecture

Araldo turns "announce this" requests from your products into published
posts on every platform your users read. This page is the map; the
[decision records](adr/) hold the reasons.

## The shape

```
 your product ──HTTP (API key)──▶ araldo server ──▶ Postgres ◀── araldo worker ──▶ Bluesky, Mastodon,
 a person     ──browser (session)─▶  (API + dashboard)             (publisher,         Discord, Telegram,
                                                                     webhooks, tasks)   sandbox …
                                                                          │
                                                                          └──signed webhooks──▶ your product
```

- **One binary, two roles** ([ADR 0002](adr/0002-layout-and-storage.md)):
  `araldo server` serves the API (`/v1`) and the dashboard; `araldo worker`
  publishes, delivers webhooks and runs housekeeping. `araldo all` runs
  both in one process.
- **Postgres is the only dependency.** The publishing queue and webhook
  queue are rows claimed with `FOR UPDATE SKIP LOCKED` and leases; periodic
  tasks use lease rows too, so any number of workers can run. Images are
  stored there too, unless S3-compatible storage is configured
  ([ADR 0017](adr/0017-media.md)).

## The life of a post

1. A product calls `POST /v1/posts` with a template and data (or finished
   text), any images it uploaded to `/v1/media`, and a time: `now`,
   `next_slot`, or a timestamp. A `next_slot` post that needs approval
   takes its slot when approved; posts can be moved or swapped until they
   start publishing ([ADR 0022](adr/0022-slots-at-approval.md)).
2. `core` renders the text for every channel with that platform's rules
   ([ADR 0010](adr/0010-templates.md)), refuses it with every problem listed
   if anything does not fit, and otherwise stores the post and one
   **target** per channel, with the text frozen. An event `post.created` is
   written in the same transaction ([ADR 0012](adr/0012-events-and-webhooks.md)).
3. If the brand requires approval, targets wait (`held`) until an admin
   approves.
4. The worker claims due targets, one per channel at a time, records the
   attempt, and calls the platform adapter
   ([ADR 0009](adr/0009-platform-adapters.md)). The outcome decides what
   happens next ([ADR 0011](adr/0011-publishing.md)): published, retried
   later, failed, or `needs_attention` when the platform may or may not
   have posted it and retrying could post it twice.
5. Each outcome is an event; webhook endpoints that subscribe get a signed
   delivery, retried for up to three days.

## Words

| Term | Meaning |
|---|---|
| Org | A tenant: members, brands, keys. Nothing crosses orgs. |
| Brand | A product or voice in an org, with its own channels, templates, slots, approval policy and the sites whose links get UTM parameters ([ADR 0016](adr/0016-link-tagging.md)). |
| Channel | A connected account on a platform, under a brand. Test mode has sandbox channels only. |
| Template | Versioned text with a JSON Schema for its data and per-platform bodies. |
| Post | Something to publish, to one or more channels. |
| Media | An uploaded image a post attaches; checked against each channel's platform. |
| Engagement | A published target's likes, reposts, replies and quotes, read on a schedule ([ADR 0018](adr/0018-engagement.md)). |
| Target | One channel's copy of a post: the unit of publishing work. |
| Event | A record of something that happened, kept 30 days, delivered to webhooks. |
| Mode | Test or live. Decided by the API key (or the dashboard switch). |

## Code layout

See [ADR 0002](adr/0002-layout-and-storage.md). In short: `internal/core`
holds every use case and is the only thing the API (`internal/api`), the
dashboard (`internal/web`) and the CLI (`internal/cli`) call;
`internal/store` holds all SQL; platform adapters live in
`internal/platform/<name>`.

## Security in one screen

- Tenancy is enforced in `core` (every query scoped to the caller's org) and
  in the schema (composite foreign keys) ([ADR 0004](adr/0004-tenancy-roles-approvals.md)).
- Stored secrets are envelope-encrypted per org and bound to their row
  ([ADR 0008](adr/0008-encryption.md)); API keys, sessions and recovery codes
  are stored as hashes.
- Sign-in uses argon2id passwords, TOTP with recovery codes, sessions with
  CSRF tokens, and re-authentication for sensitive actions
  ([ADR 0007](adr/0007-authentication-and-mfa.md)).
- Outbound requests to addresses tenants choose (webhooks, Mastodon
  servers, custom Bluesky PDSs) refuse private networks unless the
  operator allows them.
