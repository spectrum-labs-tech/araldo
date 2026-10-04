# ADR 0028: The CLI is an API client, modeled on `gh`; only server administration touches the database

- Status: accepted; step 1 built (sign-in through the dashboard, a test and a live key per server)
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

The model is the GitHub CLI (`gh`): its sign-in, credential handling and output are the standard
developers already know, so where this ADR does not say otherwise, do what `gh` does. For modes,
which `gh` has no counterpart of, the model is the Stripe CLI.

1. **Two kinds of command.**
   - **Client commands call `/v1` over HTTPS and never open the database**: `channels`, `members`,
     `org`, and later posts and templates. They work from anywhere `/v1` is reachable.
   - **Server administration lives under `araldo admin …`** and keeps direct access: `bootstrap`,
     `keys generate|rotate|status`, `users create|reset-password`, `apikeys create`. It runs where the
     deployment's configuration is (a pod, the host). It acts as **the operator**, not as a member:
     audit entries carry no member, request ID `admin-cli`, and the command's name. `--as` goes away.
   - The process commands stay top level: `server`, `worker`, `all`, `migrate` (the chart's
     migration Job runs `araldo migrate`), `mcp` and `version`.
2. **`araldo auth`, as `gh auth`:**
   - **First, the simplest sign-in that works:** `araldo auth login --hostname h` opens the
     dashboard's `/cli?device=<this computer>` page in the browser (or prints its address, over
     SSH). The page asks for the password first (sudo mode), so nothing typed is lost to the
     confirmation; then a short form (the name, "araldo CLI on <computer>", and a brand or all)
     makes a full-access key in the mode the CLI asked for (test, or live with `--live`), whatever
     mode the dashboard is showing, and shows only that key, to paste at the CLI's prompt, which
     does not echo. The CLI checks the pasted key's mode with `GET /v1/me` and refuses the wrong one.
     This is `gh`'s "paste a token" path with the page opened for you: no new endpoints. The
     credential is an API key, so commands that only members may run (`members`, `org`) wait for
     the device flow below, built when the CLI needs them.
   - **Later,** `araldo auth login` without a pasted key: the OAuth 2.0 device authorization grant
     (RFC 8628). The CLI asks `POST /v1/auth/device` for a device code and a short user code
     (`ABCD-EFGH`), prints `! First copy your one-time code: ABCD-EFGH`, offers to open the
     dashboard's `/device` page in the browser, and polls `POST /v1/auth/device/token` at the
     interval given until the code is approved, denied or expires (15 minutes). `--with-token` reads
     a token or an API key from stdin instead, for scripts and CI.
   - **A test key and a live key per server, as the Stripe CLI keeps them.** A key belongs to one
     mode ([ADR 0006](0006-test-mode-and-api-keys.md)), so the CLI keeps one of each:
     `auth login` adds the test key and `auth login --live` the live one, each replacing only its
     own. Every client command (`channels`, `api`, `mcp`, `auth token`) uses the test key unless
     given `--live`, so a command that posts reaches real accounts only when asked to, as
     `stripe --live` does. With `--with-token`, a key read from stdin goes in its own mode's place.
   - `araldo auth status` (each server, both keys, where each is stored, and whether it still
     works), `auth logout` (both keys), `auth token [--live]` (prints it, for piping), `auth switch`
     (between accounts signed in on one server).
   - The member approves in the **dashboard**, signed in, which enforces the org's two-factor policy
     and, in an install behind an access proxy, the proxy too. Approving creates a credential, so it
     needs **sudo mode** ([ADR 0007](0007-authentication-and-mfa.md) decision 4). The page shows what
     is asking: the device name the CLI sent and its IP.
   - It works the same on a laptop, over SSH and in a container, with no local web server.
3. **User tokens.**
   - `ald_user_` followed by 32 random characters (the distinctive prefix lets secret scanners find
     leaks, as with keys), shown once to the CLI, stored only as a SHA-256 hash.
   - **A token is the person, like a `gh` token**, not bound to an org: each request names the org
     (`Araldo-Org`, an ID or name), as Stripe's `Stripe-Account` header names an account. The CLI
     sends it from `--org`, or the default set with `araldo config set org …`, as `gh` remembers a
     default repository.
   - **A token is bound to one mode, like a key**: the device flow issues a test token, or a live
     one with `--live`, kept in the same two places as keys. One rule for every credential keeps
     test mode's promise (it never reaches a real account) without a header a script can forget.
   - **Never more than the member:** every request is checked against the member's current role in
     that org, so a role change or removal takes effect at once. Sudo-mode actions (creating API
     keys, changing two-factor, removing owners, deleting the org) stay dashboard-only.
   - **Valid until revoked**, expiring only after a year unused, as GitHub does for `gh`. Listed under
     **Your account → Devices** with device name, created and last used, and revocable there or with
     `araldo auth logout`. Disabling or deleting the user revokes their tokens.
   - Audited as the member, with the token's ID, so CLI changes are attributable and distinguishable
     from the dashboard's.
4. **`/v1` takes user tokens as well as keys.** A route checks the caller's permissions the same way
   for both. New routes for what only members may do, `/v1/members` and `/v1/org`, accept user
   tokens and refuse keys ([ADR 0019](0019-administration-api.md) decision 6 stands for keys).
   Tokens are bearer credentials, not cookies, so CSRF does not apply.
5. **Credentials and configuration on the client, as `gh` keeps them:**
   - the token in the **OS keychain** (macOS Keychain, Windows Credential Manager, the Secret Service
     on Linux) through `zalando/go-keyring`, the library `gh` uses: the standard library cannot reach
     a keychain, and a token on disk is the one secret a CLI most needs to protect. Without a
     keychain (a container, a headless server) it falls back to `~/.config/araldo/hosts.yaml`, mode
     0600, and `auth status` says so;
   - servers and defaults in `~/.config/araldo/` (`$ARALDO_CONFIG_DIR`, or the platform's config
     directory on Windows);
   - `ARALDO_TOKEN` (a user token or an API key) and `ARALDO_HOST` in the environment override both,
     for CI.
6. **Output, as `gh`'s:** aligned tables in a terminal and tab-separated values when piped; color
   only in a terminal (`NO_COLOR` respected); `--json [fields]` with `--jq` (`itchyny/gojq`, as `gh`)
   for scripts; errors on stderr with a non-zero exit. `araldo api <path>` makes an authenticated
   request and prints the response, as `gh api` does.
7. **Order of work:**
   1. The client foundation: hosts and credentials (a test and a live key per server), `araldo api`,
      table/JSON output, and `channels list` as a client command; `auth login` through the
      dashboard's key page. Done.
   2. The device flow, user tokens, the `/device` page and the Devices list, when member-only
      commands come to the CLI.
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
  (copying a secret by hand). `auth login --with-token` keeps that path open for unattended use.
- **Tokens bound to one org,** like keys. A person belongs to several orgs; a token per org is the
  friction `gh` avoids by making the token the person and the repository a per-command choice.
- **One credential for both modes, with the mode chosen per request** (an `Araldo-Livemode`
  header). One sign-in instead of two, but every credential on a laptop could then post to real
  accounts, and a missing header would be the only thing keeping a test run in test mode. Keys
  are already mode-bound, and the Stripe CLI shows two sign-ins is little friction.
- **Reusing dashboard session cookies.** Cookies carry CSRF rules and browser-only flags, and
  sessions are not meant to leave the browser.

## Consequences

- Routine work no longer needs cluster access: a member runs `araldo auth login` against the public
  `/v1` from their own machine, and the audit log says who did what.
- Shell access to the deployment becomes break-glass (`araldo admin`), which operators can restrict.
- The API grows a second credential type, the `Araldo-Org` header,
  `/v1/auth/device*`, `/v1/members` and `/v1/org`; the dashboard a device-approval page and a Devices
  list. The contract tests cover them.
- Two new dependencies, `zalando/go-keyring` and `itchyny/gojq`, both what `gh` uses.
- `araldo members`, `araldo org` and the `--as` flag change incompatibly. Before 1.0 that is
  acceptable; the release notes say so.
