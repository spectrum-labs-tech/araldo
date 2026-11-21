# ADR 0035: `araldo admin` is a client of the operator API; only bootstrap opens the database

- Status: accepted; built
- Date: 2026-11-20
- Supersedes: [ADR 0028](0028-cli-as-api-client.md) decision 1's second point
  ("server administration lives under `araldo admin …` and keeps direct access")
- Builds on: [ADR 0031](0031-operator-api.md) (operator keys and the operator API)

## Context

ADR 0028 made every command a member runs a client of `/v1`, and kept
server administration (`araldo admin`) on the database: it ran where the
deployment's configuration was, a shell in a pod or on the host, with the
database URL and the master keys. ADR 0031 then gave the install an
operator API (`/v1/operator/`), authenticated with operator keys, for the
service that hosts orgs.

That left two ways to act as the operator, with different reach and
different records:

- `araldo admin` needed a shell beside the database and the master keys in
  its own environment. Rotating master keys used the CLI's configuration,
  not the server's, so the two could disagree. Its audit entries named a
  command, not a credential.
- The operator API already did half of the same work (orgs, their status,
  limits and invitations) with a revocable credential, audited by key.

Most administration (accounts, members, API keys, operator keys,
install-wide apps, master key status and rotation) needs nothing the server
does not already have. Only the very first run does: before any user or
key exists, nothing can authenticate to an API.

## Decision

1. **Everything but bootstrap goes through the operator API.** `araldo
   admin users`, `apikeys`, `members`, `org`, `operator-keys`, `apps` and
   `keys rotate|status` call `/v1/operator/…` with an operator key and never
   open the database. They act as the operator (ADR 0028): an owner's
   permissions in any org, no member, no sudo mode, as before.
2. **What stays direct:**
   - `araldo admin bootstrap` creates the first user, org and brand with the
     database, as before, and with `--operator-key` also makes an operator
     key and prints only it on stdout (everything else goes to stderr), for
     piping into a secret store, as `apikeys create` does. With
     `--operator-key` alone it makes only the key: how an install that
     predates this ADR gets its first one, and the break-glass way back in
     if every key is lost.
   - `araldo admin keys generate` touches neither: it prints a random master
     key for the configuration.
   - `araldo migrate` is unchanged.
3. **The operator API grows what administration needs**, operator keys
   only, as every `/v1/operator/` route:
   - accounts: `GET /v1/operator/users?email=`, `POST /v1/operator/users`,
     `POST /v1/operator/users/{id}/password`;
   - in an org: `GET` and `POST /v1/operator/orgs/{id}/members`,
     `POST` and `DELETE /v1/operator/orgs/{id}/members/{user}`,
     `POST /v1/operator/orgs/{id}/api_keys`;
   - `GET` and `POST /v1/operator/operator_keys`,
     `DELETE /v1/operator/operator_keys/{id}`;
   - `GET` and `POST /v1/operator/apps`, `POST` and
     `DELETE /v1/operator/apps/{id}`;
   - `GET /v1/operator/master_keys`, `POST /v1/operator/master_keys/rewrap`;
   - and on what was there: `GET /v1/operator/orgs?name=` finds an org by
     name (the CLI's `--org` takes an ID or a name), and
     `POST /v1/operator/orgs/{id}` also takes `require_mfa` and
     `require_sso`, the owners' settings, so the operator can still let an
     org's members in when its identity provider is gone (ADR 0033).
4. **Secrets go in, never out.** Passwords and client secrets are sent in
   request bodies (never in a URL) and never returned. A new API key or
   operator key is returned once, as `secret`, in the response that made it.
   The CLI still generates passwords itself and prints them as before, or
   reads them from stdin; the server never generates one to return.
5. **An operator key cannot revoke itself** (`operator_key_self`, 409), so
   an install always keeps a key that can make more; another key revokes
   it, and `bootstrap --operator-key` makes one if all are lost.
6. **Master key rotation is a bounded batch.** `POST
   /v1/operator/master_keys/rewrap` rewraps up to `batch_size` data keys
   (default 100, at most 1,000) under the server's primary master key and
   returns how many it moved and how many remain; `araldo admin keys rotate`
   calls it until none remain. Each call is short, idempotent (a key already
   under the primary is never touched again) and safe to interrupt and
   rerun. Data keys under a master key the server does not have are counted
   (`unconfigured`) and skipped rather than failing the batch; the CLI then
   exits non-zero, as `keys status` does. Rotation now uses the *server's*
   master keys: restart the server with the new primary first.
7. **The CLI's configuration**, following the client commands' conventions
   (ADR 0028):
   - the operator key from `ARALDO_OPERATOR_KEY` only, never a flag, so it
     stays out of shell history and process listings (stdin is taken by
     passwords and client secrets);
   - the server from `--hostname`, else `ARALDO_OPERATOR_HOST`, else the
     server on this machine at `ARALDO_LISTEN` (`http://127.0.0.1:8080`). It
     never falls back to `ARALDO_HOST` or the server signed in to with
     `araldo auth login`: that is the public hostname, and the operator API
     belongs on an internal address (ADR 0031's "keep it off the
     internet"). A bare host name means HTTPS, as with `--hostname`
     elsewhere; an internal address usually needs `http://`.
   - `araldo admin` help says which commands need the database and which an
     operator key.
8. **Audit.** Changes over the operator API are audited as today's operator
   API changes are (ADR 0031): no member, the operator key's ID
   (`detail.operator_key`) and the request's ID, instead of the command name
   and the request ID `admin-cli`. Bootstrap, on the database, still records
   its command. A batch that rewraps data keys records `master_keys.rewrap`.
9. **Compatibility.** Output, flags and exit codes stay as they were, except:
   - every command but bootstrap and `keys generate` needs
     `ARALDO_OPERATOR_KEY` and a reachable server, and no longer reads
     `ARALDO_DATABASE_URL` or the master keys;
   - `--as` (on `members` and `org update`) and `apikeys create --email`,
     deprecated since ADR 0028, are refused with a usage error (exit 2):
     name the org with `--org`;
   - `keys rotate` reports progress per batch on stderr, takes
     `--batch-size`, and exits 1 when data keys are under a master key the
     server lacks, after rotating the rest;
   - every admin command takes `--hostname`.

## Alternatives considered

- **Keep the direct database access** (ADR 0028). It needs a shell beside
  the database and the master keys in the CLI's environment, rotates with
  the CLI's keys rather than the server's, and audits a command rather than
  a revocable credential.
- **A separate admin API with its own credential.** The operator API
  already is that, with keys, audit and the "off the internet" guidance; a
  second one would split the same powers across two front doors.
- **Server-generated passwords returned once.** The CLI already generates
  readable passwords and offers `--password-stdin`; returning a password
  from the API would add a secret to responses for no gain.
- **A single long rewrap request.** On a large install it can outlast a
  proxy's timeout and, cut off, leaves the caller unsure what happened.
  Batches make progress visible and resumable.
- **Defaulting the server to the signed-in host.** Convenient, but it
  would send the operator key to the public hostname, the opposite of
  keeping the operator API internal.
- **Taking the operator key from a flag or stdin.** A flag leaks into
  history and process lists; stdin already carries passwords and secrets.

## Consequences

- Shell access to the deployment is needed only for the first run, and
  for break-glass (`bootstrap --operator-key`); routine administration
  works from anywhere that reaches the internal operator API.
- Operator keys become the one credential for administration, so they must
  be guarded and kept off the internet as ADR 0031 says.
- The operator API is larger, and its contract tests cover it.
- Installs that predate this make their first operator key with
  `araldo admin bootstrap --operator-key` before using the other admin
  commands; the release notes say so.
