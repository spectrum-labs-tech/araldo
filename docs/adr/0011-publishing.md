# ADR 0011: Publishing is an outbox with leases, and it never double-posts silently

- Status: proposed
- Date: 2026-09-28

## Context

A scheduled post must go out close to its time, once, on every channel,
even if the worker is restarted in the middle. Almost no social API accepts
an idempotency key: if a request times out after sending, the post may or
may not exist, and simply retrying can publish it twice, publicly.

Polling a job table every few minutes is the common shape, and too coarse.
We want lease-based queues instead: a worker claims a row for a while,
and a crashed worker's claim simply expires.

## Decision

1. **A post has one target per channel.** The target is the unit of work
   (an outbox row). The post's status is derived from its targets: `draft`,
   `pending_approval`, `scheduled`, `publishing`, `published`,
   `partially_published`, `failed`, `canceled`.
2. **Scheduling:**
   - `publish_at` is a time, `"now"`, or `"next_slot"`. A time up to 15
     minutes past means now (a late clock); older is refused, so a
     mistyped year cannot publish at once. Each brand has
     weekly slots in its time zone (for example weekdays at 09:00 and
     13:00); `next_slot` takes the earliest free slot for each channel,
     guarded by a unique constraint so two posts cannot take the same slot.
     A post that needs approval takes its slot when approved, and posts
     can be moved ([ADR 0022](0022-slots-at-approval.md)).
   - Each target has a **`publish_by` deadline** (by default
     `publish_at` plus 24 hours). Past it, the target fails as `expired`
     instead of posting stale content ("today's featured build", three days
     late).
3. **The worker claims due targets** with `FOR UPDATE SKIP LOCKED` and a
   lease, polling every 5 seconds and woken early by `LISTEN/NOTIFY` when a
   post is due now. It never runs two targets on the same channel at once,
   and after a rate limit it holds that channel until the time the platform
   gave.
4. **Delivery is at most once unless we know otherwise.** A target moves
   through:

   ```
   queued ──claim──▶ publishing ──ok──▶ published
      ▲                  │
      └── retryable ─────┤  (rate limit, transient, failed before sending)
                         ├── rejected / auth revoked / expired ──▶ failed
                         └── timeout after sending, or lease expired ──▶ uncertain
   ```

   - The worker commits `publishing` and an attempt record *before* it
     calls the platform, so a crash leaves evidence, not a blind retry.
   - **`uncertain`** is resolved by reconciliation when the adapter has a
     `Finder`: it searches the channel's recent posts for this target's
     content. If found, the target is published; if not, it is retried once
     more. Without a `Finder`, the target waits in `needs_attention`, emits
     `post_target.uncertain`, and a person (or the caller, through the
     API) chooses `retry` or `mark_published`.
   - Retries back off exponentially with jitter and stop at `publish_by`.
5. **Threads** publish their parts in order, and each part's remote ID is
   stored as it succeeds, so a retry continues from the first missing part.
6. **Changes:** a queued target can be edited (re-rendered and
   re-validated) or canceled. Deleting published posts on the platform
   comes later, where the platform allows it.
7. **Periodic work** runs as `opsched` tasks: refreshing tokens, checking
   channels, reclaiming expired leases, reconciling uncertain targets,
   pruning idempotency keys and old events, and backups.

## Alternatives considered

- **At-least-once with blind retries.** Fine for webhooks, where receivers
  deduplicate; unacceptable for public posts.
- **River or Temporal.** A second copy of the state (job row and target
  row) to keep consistent, and a dependency the existing lease pattern makes
  unnecessary.
- **Polling every few minutes.** Too coarse for "publish at
  9:00".

## Consequences

- Some posts will need a person to decide, and the dashboard and API must
  make that quick. It is the price of never double-posting silently.
- Adapters that can implement `Finder` should, because it turns most
  `uncertain` cases into automatic answers.
- The sandbox's `timeout_after_send` simulation exercises this path in
  every client's test suite.
