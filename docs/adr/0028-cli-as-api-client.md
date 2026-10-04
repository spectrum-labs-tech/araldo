# ADR 0028: The CLI is an API client; members sign in with a device code, and only server administration touches the database

- Status: proposed
- Date: 2026-10-04
- Supersedes: [ADR 0019](0019-administration-api.md) decision 1 ("the CLI takes operators with
  database access; there is no session-authenticated API") and decision 6's CLI half

## Context

Today every CLI command opens the database and the master keys directly, and commands a member
would run (`araldo members`, `araldo org`, `araldo channels`) act as whoever `--as <email>` names.
Nothing proves the person at the keyboard is that member, two-factor never comes into it, and the
audit log records the named member rather than who ran the command. The real gate is access to
the deployment itself (a shell in a pod, or the database URL), so the CLI only works from inside it.

Two kinds of command are mixed together:

- **Server administration**, which has to work when the API cannot help: migrating the schema,
  creating the first account before anyone can sign in, rotating master keys, resetting a locked-out
  owner's password, minting the first API key. Every self-hosted product has this (a Rails console,
  `gitlab-rake`), run by whoever operates the server.
- **Everything a member or an integration does**: channels, members, org settings, and later posts
  and templates. These have a front door already, `/v1`, with real authentication, roles, scopes
  and an audit trail, and `araldo mcp` already uses it as a client.

API keys alone cannot carry the second kind: members and org settings deliberately take no key
scope ([ADR 0019](0019-administration-api.md) decision 6), and a long-lived org key on a laptop is
the wrong credential for a person.

## Decision

1. **Two kinds of command.**
   - **Client commands call `/v1` over HTTPS and never open the database.** They read
     `ARALDO_URL` and a credential: a member token from `araldo login`, or an API key
     (`ARALDO_API_KEY`, as `araldo mcp` does) for scripts. They work from anywhere `/v1` is reachable.
   - **Server administration lives under `araldo admin …`** and keeps direct access: `bootstrap`,
     `keys generate|rotate|status`, `users create|reset-password`, `apikeys create`. It runs where the
     deployment's configuration is (a pod, the host). It acts as **the operator**, not as a member:
     audit entries carry no member, request ID `admin-cli`, and the command's name. `--as` goes away.
   - The process commands stay top level: `server`, `worker`, `all`, `migrate` (the chart's
     migration Job runs `araldo migrate`), and `mcp` and `version`.
2. **`araldo login` uses the OAuth 2.0 device authorization grant (RFC 8628).**
   - The CLI asks `POST /v1/auth/device` for a device code, a short user code (eight letters,
     `ABCD-EFGH`) and the dashboard page to approve it at (`/device`). It prints the code and the
     URL, and polls `POST /v1/auth/device/token` at the interval given until the code is approved,
     denied or expires (10 minutes).
   - The member approves in the **dashboard**, signed in, which already enforces the org's
     two-factor policy and, in an install behind an access proxy, the proxy too. Approving creates a
     credential, so it needs **sudo mode** ([ADR 0007](0007-authentication-and-mfa.md) decision 4).
     The page shows what is asking (the device name the CLI sent, its IP) and which org and mode the
     token is for, and the member chooses.
   - It works the same on a laptop, over SSH and in a container, with no local web server.
3. **Member tokens.**
   - `ald_user_` followed by 32 random characters (the distinctive prefix lets secret scanners
     find leaks, as with keys), shown once to the CLI, stored only as a SHA-256 hash.
   - Bound to **one member, one org and one mode** (test or live), like a key; `araldo login --live`
     asks for a live one.
   - **They are the member, never more:** every request is checked against the member's current
     role, so a role change or removal takes effect at once. Sudo-mode actions (creating API keys,
     changing two-factor, removing owners, deleting the org) stay dashboard-only.
   - Expire after 30 days, or 7 days unused; `araldo login` again to renew. Listed under
     **Your account → Devices** with device name, created and last used, and revocable there or
     with `araldo logout`. Removing a member revokes their tokens.
   - Audited as the member, with the token's ID, so CLI changes are both attributable and
     distinguishable from the dashboard's.
4. **`/v1` takes member tokens as well as keys.** A route checks the caller's permissions the same
   way for both. New routes for what only members may do, `/v1/members` and `/v1/org`, accept
   member tokens and refuse keys ([ADR 0019](0019-administration-api.md) decision 6 stands for
   keys). Tokens are bearer credentials, not cookies, so CSRF does not apply.
5. **On the client,** `araldo login` stores the token in `~/.config/araldo/credentials.json` (mode
   0600) under a profile per server (`--profile`, default `default`); `ARALDO_TOKEN` or
   `ARALDO_API_KEY` in the environment override it. An OS keychain can come later.
6. **Order of work:**
   1. `channels list` becomes a client command now: `GET /v1/channels` already takes a key.
   2. The device flow, member tokens and the Devices page.
   3. `/v1/members` and `/v1/org`; `members` and `org` become client commands; `--as` is removed.
   4. Server administration moves under `araldo admin`, audited as the operator.

## Alternatives considered

- **Keep the direct-database CLI.** Simple, but it impersonates members, skips two-factor, audits
  the wrong person, and needs a shell in the deployment for routine work.
- **API keys only.** Members and org settings take no key scope on purpose, and an org-wide key is
  the wrong credential to keep on a laptop for a person.
- **Browser sign-in with a localhost callback (PKCE).** Smooth on a desktop, but fails over SSH and
  in containers, where operators often are. The device flow works everywhere.
- **Personal access tokens pasted from the dashboard.** The same token, with a worse handoff
  (copying a secret by hand). A pasted token remains possible for unattended use through an API key.
- **Reusing dashboard session cookies.** Cookies carry CSRF rules and browser-only flags, and
  sessions are not meant to leave the browser.

## Consequences

- Routine work no longer needs cluster access: a member runs `araldo login` against the public
  `/v1` from their own machine, and the audit log says who did what.
- Shell access to the deployment becomes break-glass (`araldo admin`), which operators can restrict.
- The API grows a second credential type, `/v1/auth/device*`, `/v1/members` and `/v1/org`, and the
  dashboard a device-approval page and a Devices list; the contract tests cover them.
- `araldo members`, `araldo org` and the `--as` flag change incompatibly. Before 1.0 that is
  acceptable; the release notes say so.
