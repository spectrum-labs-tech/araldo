# ADR 0004: Orgs are the tenant boundary; brands group channels; four roles and optional approvals

- Status: accepted; built, except auditing denials (brand-limited members remain deferred)
- Date: 2026-09-28

## Context

One operator often runs several products (Spectrum Labs runs Araldo and
open-b00ks, for example), each with its own accounts, voice and schedule,
and one team across all of them. A hosted Araldo would also serve unrelated
customers who must never see each other's data. A leak between tenants is
the worst bug this system can have: it could publish one customer's content
on another customer's account.

## Decision

1. **Org** is the tenant boundary (and, if hosted, the billing unit). Every
   tenant-owned row carries `org_id`.
2. **Brand** is a product or voice inside an org. Channels, templates,
   schedule slots and the approval policy belong to a brand. An org can have
   many brands; Araldo and open-b00ks are brands of one Spectrum Labs
   org.
3. **Users are global** (one login, many orgs, as on GitHub). A
   **membership** gives a user one role in one org:

   | Role | Can |
   |---|---|
   | owner | everything, including deleting the org and managing owners |
   | admin | members, API keys, channels, webhooks, brand settings, approve posts |
   | editor | create and edit templates and posts, schedule (subject to approval) |
   | viewer | read everything except secrets |

   Restricting a member to some brands is a later addition.
4. **Approvals** are a brand setting: `none`, `required_for_editors_and_keys`
   or `required_for_all`. A post that needs approval waits in
   `pending_approval` until an admin or owner approves or rejects it.
   The approval, the approver and the time are recorded on the post.
   A **template can override** the brand's policy: `inherit` (the default),
   `required` or `not_required`, so a routine announcement can go straight
   out while everything else waits, or the reverse. Only people who can
   approve posts may set an override; an editor or an API key cannot exempt
   its own posts. There is no per-post override for the same reason.
5. **Enforcement is layered:**
   - **Services** receive the caller (a membership or an API key) from the
     context and scope every store call to its org. A record in another org
     is `store.ErrNotFound`, never "forbidden", so its existence does not
     leak. A disallowed action in the caller's own org is
     `ErrForbidden`.
   - **Every store query** filters by `org_id`; store methods take it as a
     required parameter.
   - **Composite foreign keys** make cross-tenant references impossible in
     the database itself: child tables reference `(org_id, id)`, so a post
     target cannot point at another org's channel even if a query forgets a
     filter.
   - **Tests:** every service operation has a test that calls it as a
     member of another org and expects `ErrNotFound`.
6. **Audit log:** every change (and every denial) records an audit event
   (actor, org, action, target, request ID) in the same transaction as the
   change. Content values are not copied into audit details.

## Alternatives considered

- **One org per product.** Simpler model, but members, keys and billing
  would be repeated for every product one team runs.
- **Postgres row-level security** as the primary guard. Strong, but with
  pooled connections every transaction must set the tenant, mistakes fail
  closed in confusing ways, and it complicates the worker, which works
  across orgs. Composite keys plus service tests cover the risk for now;
  revisit RLS as defense in depth before a hosted launch.
- **A database or schema per tenant.** Strong isolation, heavy operations,
  and it rules out one worker serving every tenant.

## Consequences

- Every new table needs `org_id`, a composite key on `(org_id, id)` where
  children reference it, and a test from another org.
- The worker is the one component that reads across orgs; it only claims
  work and hands each item to the service layer under that item's org.
