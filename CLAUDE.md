# araldo: notes for AI assistants

Developer-first social distribution API (schedule and publish posts across
platforms from templates). Read `docs/architecture.md`, then the ADRs in
`docs/adr/`. `CONTRIBUTING.md` applies to you too.

## Ground rules

- **This repo is (going to be) public.** Never reference private
  deployments, deploy repos, infrastructure repos, hostnames or secrets
  paths. Deployments pull published images and charts; nothing here
  triggers or names them.

- **Layering** ([ADR 0002](docs/adr/0002-layout-and-storage.md)):
  - `internal/model` has no storage or transport concerns.
  - `internal/store` is the only package with SQL (pgx, Postgres only).
  - `internal/core` holds every use case. The API (`internal/api`),
    dashboard (`internal/web`) and CLI (`internal/cli`) call `core`, never
    the store.
  - `cmd/araldo/main.go` only calls `cli.Run`.
- **Tenancy** ([ADR 0004](docs/adr/0004-tenancy-roles-approvals.md)): every
  `core` operation takes an `Actor` and scopes every store call to
  `a.OrgID`. Another org's record is `apperr.NotFound`, never forbidden.
  New tables get `org_id` and composite `(org_id, id)` foreign keys. New
  operations get a cross-org test in `internal/core/core_integration_test.go`.
- **Modes** ([ADR 0006](docs/adr/0006-test-mode-and-api-keys.md)): activity
  tables carry `livemode`; test mode only ever touches sandbox channels.
- **Secrets** ([ADR 0008](docs/adr/0008-encryption.md)): encrypt with
  `keyring` using `keyring.AAD(table, column, rowID)`. Never log, return or
  put in an event a token, key, secret or credential.
- **Publishing** ([ADR 0011](docs/adr/0011-publishing.md)): never retry an
  uncertain attempt on a non-idempotent platform; it goes to
  `needs_attention`.
- **Events** are written with `s.emit` in the same transaction as the
  change, using the `core.View*` shapes the API returns.
- **Platforms** ([ADR 0009](docs/adr/0009-platform-adapters.md)): one package
  per adapter under `internal/platform/`, classified errors (`platform.Error`),
  tests against `httptest` servers only. Every limit in `rules.go` cites
  its source.
- **Migrations**: golang-migrate in `internal/store/migrations`. Never edit a
  shipped migration.
- **Styles** ([ADR 0015](docs/adr/0015-dashboard.md)): Tailwind CSS. Edit
  `internal/web/styles/app.css` and templates, run `task web:css`, and commit
  `internal/web/static/app.css` (CI checks it is current). Never edit the
  compiled file. Layout uses utilities; anything repeated is a component
  class in the source stylesheet. Colors are semantic tokens (`bg-panel`,
  `text-muted`, `border-line`…) so dark mode keeps working.
- **Scripts**: `internal/web/static/app.js` is small hand-written progressive
  enhancement. The template editor (CodeMirror) is bundled from
  `internal/web/scripts/editor.js` by `task web:js`; commit
  `internal/web/static/editor.js` (CI checks it too). Pages must work
  without JavaScript. Styles that scripts inject need the per-request CSP
  nonce (`data-nonce`); never loosen the policy with `unsafe-inline`.
- **Context** flows end to end; logging is `log/slog` only.
- **Every Go file starts with** `// SPDX-License-Identifier: AGPL-3.0-or-later`.
- **Dependencies**: standard library first; justify any new module.

## Testing

- `task check` must pass with no network or database.
- Integration tests (`//go:build integration`) need `task db:up`, then
  `task test:integration`. They run in parallel against a database full of
  earlier runs' data, and must pass rerun after rerun:
  - Create your own org, users and brands with random names; never rely on
    rows you did not create, and never change them.
  - Assert only on your own rows: no global counts, no "the queue is empty".
  - Workers (publishing, webhook delivery) claim due work in every org, and
    other tests run them at the same time. Drive them in a loop and wait for
    your own post or delivery to reach its state (see `settle`); never assume
    your call did the work.
  - Never clean up.
- Table-driven tests and `t.Parallel()` everywhere; a test that cannot run
  in parallel says why in a comment.

## Commands

All recurring commands live in `Taskfile.yaml` (`task --list`).
