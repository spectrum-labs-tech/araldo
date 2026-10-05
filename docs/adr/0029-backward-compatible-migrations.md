# ADR 0029: Migrations work with the release still running, and a test enforces it

- Status: accepted; built (the rules apply from migration 000015; `TestMigrationsAreBackwardCompatible` checks them)
- Date: 2026-10-04

## Context

The Helm chart migrates in a hook Job before the Deployments roll, so the
previous release keeps serving on the new schema until its pods are
replaced, and keeps serving indefinitely if the rollout stalls. A pod only
turns unready when the schema is *behind* its binary, so the old pods stay
in service. `docs/operations.md` asked for migrations the previous version
can run against, but nothing checked it, and some shipped migrations did
not hold to it:

- `000008` dropped `NOT NULL` from `posts.publish_at` and set it to `NULL`
  on pending posts, while the release before it read the column as never
  null.
- `000007` and `000008` add `CHECK` and `FOREIGN KEY` constraints that scan
  their table while holding a lock that blocks writes, with no
  `lock_timeout`, so a busy table would stall every request behind them.

GitHub, GitLab and Stripe treat this as a rule with tooling behind it
(expand and contract, `lock_timeout`, concurrent indexes), not a hope.

## Decision

1. **Expand, then contract.** A migration may only make changes the
   previous release still works with: add a table, a nullable column or one
   with a default, an index, a constraint the previous release already
   honours. Removing or renaming a column, or making one stricter, happens
   in a later release, once no running release reads or writes it the old
   way:
   - first release: add the new shape, write both, read the new one;
   - next release: drop the old shape.
2. **Never block writes for long.** Each migration starts with
   `SET LOCAL lock_timeout = '5s';`, so it fails and can be retried rather
   than queue every request behind it. (`LOCAL`: golang-migrate sends a
   file as one query, which Postgres runs as one transaction, on a
   connection the application's pool then reuses.) A new `CHECK` or
   `FOREIGN KEY` on an existing table is added `NOT VALID` (no scan, a brief
   lock), and checked with `VALIDATE CONSTRAINT`, which lets writes
   continue, in the next migration: in the same one it would scan under the
   lock the `ADD` took. An index on an existing table is built `CONCURRENTLY`, as the
   only statement in its migration, since `CONCURRENTLY` refuses to run in
   a transaction; such a file needs no `lock_timeout`. A foreign key can
   reference a unique index directly, so a new unique key needs no
   `ADD CONSTRAINT ... UNIQUE`, which would build its index under a lock.
3. **A contracting change says so.** A statement that removes or tightens
   (`DROP COLUMN`, `DROP TABLE`, `RENAME`, `SET NOT NULL`, `ALTER ... TYPE`)
   is allowed only on a line after a comment beginning `-- contract:` that
   names the release that stopped using the old shape.
4. **A test enforces 2 and 3** on every migration from `000015` on
   (`internal/store`), with no database. Migrations before it shipped and
   are never edited.
5. **Down migrations** stay as they are: written, never run automatically.
   Rolling back is running the previous release on the newer schema, which
   rule 1 makes safe.

## Alternatives considered

- **Migrate after the rollout, or from the new pods.** Then the new release
  would serve, briefly, on the old schema: the same problem the other way
  round, with the new code (the one that changed) on the wrong side.
- **Stop the old release before migrating** (recreate, not roll). Simple,
  but every deploy is downtime, and a failed migration leaves nothing
  serving.
- **A linter such as squawk.** Thorough, but another tool in CI for rules
  a short test covers.

## Consequences

- Some changes take two releases. That is the cost of deploys without
  downtime, and the reason rule 3 makes the second step visible.
- A migration can fail on a busy database because of `lock_timeout`; the
  hook Job fails and the previous release keeps serving: a pod is ready on
  its own schema or a newer one, even one whose last migration failed.
  Retrying the deploy reruns the failed migration, since `araldo migrate`
  treats a dirty schema as that migration not having run: each migration is
  one transaction, and the invalid index a failed `CONCURRENTLY` leaves is
  dropped first. Migrations from several processes take turns on an
  advisory lock.
- Reviewers check rule 1 by reading; the test only catches the mechanical
  parts.
