# ADR 0022: A post takes its publishing slot when it is approved, and posts can be moved

- Status: accepted; built
- Date: 2026-10-02

## Context

[ADR 0011](0011-publishing.md) gave `next_slot` posts the brand's earliest
free slot when they were created, approval or not. With a review step in
between, that goes wrong in two ways:

- A post waiting for review holds a slot it may never use. Drafts queue
  up ahead of approved work, and a rejected draft leaves a gap.
- Review takes time. A draft created Monday for Monday 13:00 and approved
  Tuesday has already missed its slot; it publishes late or expires.

Consumers (an agent drafting posts for people to review, for example)
want the order of the schedule to be the order things were approved in.
People also need to move posts once they are scheduled: to another time,
another slot, or trading places with another post.

## Decision

1. **A `next_slot` post that needs approval holds no slot until it is
   approved.** While it waits, `publish_at` is null and `slot` is true.
   `publish_by` is null too, unless the caller gave one; then it only has
   to be in the future.
2. **Approving takes the brand's next free slot after the approval**, in
   the same transaction as the approval, with the same unique constraint
   and retries as creating a post. So slot order is approval order.
   - `publish_by` is the slot plus 24 hours, unless the caller gave one;
     then the slot must come before it, or approval fails with
     `409 no_slot_before_publish_by`.
   - With every slot in the search window taken, approval fails with
     `409 slots_full`.
   - Either way the post stays pending: nothing is half-approved.
   - `post.approved` carries the time it was given.
3. **Posts with a time, and posts that need no approval, are unchanged.**
4. **A post can be moved** (`POST /v1/posts/{id}/reschedule`, `posts:write`)
   while it is `scheduled` or `pending_approval` and no target has been
   tried:
   - to `"now"` or a time; a time on one of the brand's slots takes that
     slot, or fails with `409 slot_taken` naming the post that holds it;
   - to `"next_slot"`; a pending post goes back to taking its slot when
     approved;
   - or swapped with another movable post of the same brand and mode: the
     two trade times, deadlines and slots in one transaction.

   Moving keeps approval: it changes when, not what. It is audited and
   emits `post.rescheduled`.
5. **Existing pending posts release their slots** in the migration that
   makes the times nullable. A `publish_by` other than `publish_at` plus
   24 hours was the caller's, and is kept.

## Alternatives considered

- **Keep the slot from creation and re-slot at approval if it has
  passed.** The pending post still blocks approved work in the meantime,
  which is the main complaint.
- **Pick the slot when the post was created, but only claim it at
  approval.** Order would follow creation, not approval, and the chosen
  slot can be gone by then.
- **A placeholder `publish_at` (the creation time) instead of null.**
  Consumers would read a time that will not happen.
- **Editing `publish_at` with `PATCH /v1/posts/{id}`.** Posts are frozen
  once created ([ADR 0011](0011-publishing.md)); a narrow verb for the one
  thing that may change, with swap as an atomic option, keeps that true.

## Consequences

- `Post.publish_at` and `publish_by`, and `PostTarget.publish_by`, are
  nullable in the contract. Clients must handle null for pending slot
  posts.
- Approval can now fail on scheduling as well as on permissions.
- A schedule can be rearranged without canceling and recreating posts, so
  previews, approvals and history survive.
