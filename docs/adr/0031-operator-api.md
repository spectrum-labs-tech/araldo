# ADR 0031: An operator API, org limits and status, and links out for accounts and billing

- Status: accepted; built
- Date: 2026-10-05
- Builds on: [ADR 0004](0004-tenancy-roles-approvals.md) (orgs),
  [ADR 0028](0028-cli-as-api-client.md) (the operator), [ADR 0030](0030-install-wide-apps.md)
  (apps for every org)

## Context

An install that hosts orgs for other people (a hosted plan, an agency
running Araldo for its clients) needs things a single team's install does
not: orgs created by something other than their first member, limits on
what each org may use, a way to make an org read-only or stop it, and a
place to send people to sign up and pay.

Accounts and billing differ from one host to the next: payment providers,
plans, prices, trials, taxes. None of that belongs in Araldo. What Araldo
can offer is the small set of mechanisms any such system needs, so the
host's own service can drive Araldo from outside, through an API, like
any other client.

## Decision

1. **Operator keys** (`ald_op_…`, made and revoked with `araldo admin
   operator-keys`) authenticate the **operator API** under
   `/v1/operator/`. They act as the operator (ADR 0028) on the whole
   install, and nothing else: they cannot call the org API, and org keys
   and user tokens cannot call the operator API. Every change through one
   is audited with the key's ID.
2. **The operator API manages orgs**: create one (with an invitation for
   its first owner, who sets their own password and two-factor
   authentication in Araldo), read and list them, change their name,
   limits and status, invite people, read their usage, and delete them.
   An org can carry an **external reference**, unique on the install, so
   the host's system finds its org without keeping Araldo's IDs.
3. **Limits** are per org, each unlimited unless set: brands, live
   channels, members (with open invitations), and live posts a calendar
   month (UTC). Araldo enforces them where each is created and refuses
   with `limit_reached`, saying which limit. Lowering a limit below what an
   org has removes nothing; it only stops more.
4. **Status** is per org: `active`; `read_only`, where members and keys
   can read but change nothing, and scheduled posts still go out; or
   `suspended`, where keys and tokens are refused, members see only a
   notice, and nothing is published or sent. A status can carry a note,
   shown to the org. The operator (CLI or API) always can act.
5. **Links out**, each off unless set:
   - `ARALDO_SIGNUP_URL`: the sign-in page links there to create an
     account, and members can no longer create orgs themselves; orgs come
     from the operator.
   - `ARALDO_BILLING_URL`: owners see a Billing link. It goes through
     Araldo, which sends them on with a signed, five-minute hand-off
     (`ARALDO_BILLING_LINK_KEY`) naming the org, its external reference,
     the person and their role, so the host's service knows who arrived
     without its own sign-in.
6. **Usage** is read per org and month: brands, live channels, members,
   live posts created and targets published, and stored media bytes.

## Alternatives considered

- **Billing in Araldo** (plans, a payment provider, checkout). Every host
  would carry one provider's model, and self-hosters a feature they never
  use.
- **Sign-up in Araldo, open to anyone.** A host would still need to tie
  each new org to a customer and a plan; with sign-up outside, the org is
  made once the host is ready for it, and Araldo keeps the passwords.
- **Org API keys with an operator scope.** A key belongs to an org; an
  install-wide credential tied to one org would blur the boundary
  ADR 0004 draws.
- **Single sign-on into the host's service** (Araldo as an OIDC provider).
  Heavier than a signed link for one page an owner visits rarely.

## Consequences

- A host runs Araldo unchanged and keeps accounts, plans and payments in
  its own service, which holds an operator key and the link key.
- An install with none of this set behaves as before.
- Org creation, limits and status are audited like any change, so a
  member can see why their org changed.

## The hand-off

The Billing link redirects to `ARALDO_BILLING_URL` with `token=` added:
`v1.<payload>.<signature>`, where the payload is base64url (unpadded) JSON
and the signature is HMAC-SHA256, keyed with `ARALDO_BILLING_LINK_KEY`,
over `v1.<payload>`, also base64url. The payload holds `org` (the org's
ID), `external_ref` (empty if unset), `user`, `email`, `role`, `iat` and
`exp` (Unix seconds; `exp` is five minutes after `iat`). The receiver
checks the signature in constant time and the expiry, and should accept a
token once.
