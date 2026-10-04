# ADR 0002: One module, one binary, Postgres only, a fixed layout

- Status: proposed
- Date: 2026-09-28

## Context

Araldo must be easy to self-host ("one binary and a Postgres") and easy for
contributors to find their way around. The author's other Go services
(open-b00ks, otium) settled on conventions worth keeping: an
`internal/` tree, hand-written SQL, golang-migrate, model and storage kept
apart, and a Taskfile for every command.

## Decision

1. **One Go module** (`github.com/spectrum-labs-tech/araldo`) and **one
   binary**, `araldo`, with subcommands:
   - operators: `server`, `worker`, `migrate`, `backup`, `keys`, `users`;
   - developers: `posts`, `templates`, `listen`, `login` (talking to an
     Araldo API with a key).

   A deployment runs the same image as `araldo server` and `araldo worker`.
   `cmd/araldo/main.go` only calls `internal/cli`.
2. **PostgreSQL only.** We use `pgx/v5` directly with hand-written SQL, no
   ORM or query builder. Being Postgres-only lets us rely on
   `FOR UPDATE SKIP LOCKED`, `LISTEN/NOTIFY`, `jsonb`, and composite foreign
   keys ([ADR 0004](0004-tenancy-roles-approvals.md)).
3. **Migrations** use golang-migrate, embedded in the binary
   (`araldo migrate up`). A shipped migration is never edited. Each index on
   an existing table is its own migration built `CONCURRENTLY`, so upgrades
   never block writes for long.
4. **Layout:**

   ```
   cmd/araldo/              main: wires the CLI and nothing else
   internal/
     app/                   composition root shared by subcommands
     cli/                   subcommands
     model/                 domain types; no storage tags, no SQL
     store/                 all SQL (pgx), sentinel errors, migrations
     core/                  every use case: sign-in, tenancy, keys,
                            channels, templates, posts, publishing,
                            events, webhooks
     platform/              adapter interface, rules, registry
       sandbox/ bluesky/ mastodon/ discord/ telegram/ ...
     tmpl/                  template compiling and rendering
     keyring/ authn/        envelope encryption, sign-in primitives
     opsched/               periodic background tasks under leases
     api/                   HTTP API handlers
     web/                   dashboard (ADR 0015)
     apperr/ config/ id/ buildinfo/
   api/openapi.yaml         the API contract
   deploy/                  compose.yaml, helm/araldo
   docs/                    architecture, ADRs, roadmap, operations
   ```
5. **Dependency rules**:
   - `model` imports nothing internal except `platform` (for provider
     names);
   - `store` is the only package with SQL. It is concrete (Postgres only),
     with no interface layer in front of it; `core` tests run against a
     real database;
   - handlers (`api`, `web`, `cli`) never call the store; they call `core`,
     which applies tenancy ([ADR 0004](0004-tenancy-roles-approvals.md)).
6. **Background work** is an `opsched` task (in `internal/`, to move to
   `go-toolkit` once its API settles), or one of the two lease-based queues in `core`: publishing
   targets and webhook deliveries.
7. **Code rules**:
   - every function that does I/O takes `ctx context.Context` first;
   - logging is `log/slog` only;
   - every Go file starts with `// SPDX-License-Identifier: AGPL-3.0-or-later`;
   - standard library first, and any new module is justified in its pull
     request.

## Alternatives considered

- **Separate server and worker binaries.** Clearer process
  boundaries, but more to download and package. One binary with
  subcommands, run as two Deployments, gives the same isolation at runtime.
- **Supporting SQLite as well**, for a zero-dependency demo mode. Database
  portability has a real cost; Araldo's queue relies on
  Postgres features. Revisit if "try it in 30 seconds" becomes a goal.
- **sqlx or sqlc.** sqlx adds little on top of pgx; sqlc's generated code
  would sit awkwardly with the separation between model and store.

## Consequences

- Self-hosting is one image plus one Postgres (and S3-compatible storage
  once media is supported).
- Contributors need only Go and Task for the unit suite; integration tests
  need Docker for Postgres.
- Postgres-specific SQL is allowed everywhere in `pgstore`.
