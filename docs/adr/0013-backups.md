# ADR 0013: Built-in encrypted backups that the server itself cannot read

- Status: withdrawn 2026-10-05: backups are the operator's (see below); not built
- Date: 2026-09-28

## Withdrawn

Araldo will not build its own backups. Backing up a Postgres database, and
a bucket if media is in one, is what an operator's platform already does
(managed snapshots, pgBackRest or WAL-G, a streaming replica), with better
recovery points than a nightly dump, and most deployments already have
it. A built-in backup would have meant a second image with `pg_dump`, two
new dependencies and offline identities to manage, for a job better done
around Araldo than inside it. `docs/operations.md` ("Backups") says what to
back up and how to restore; what follows is the decision as it stood.

## Context

Self-hosters often have no backup system until the day they need one. The
database holds everything that matters: channels, templates, the schedule,
history. A backup must be one command, go off the machine, and be useless to
anyone who steals it, including an attacker who has taken over the server
that made it.

Installs that already back up their Postgres at the platform level can rely
on that. The built-in backup is for everyone else, and a second line of
defense.

## Decision

1. **`araldo backup create`** runs `pg_dump` (custom format, no owners),
   encrypts the stream with [age](https://age-encryption.org) to one or more
   X25519 recipients (`ARALDO_BACKUP_RECIPIENTS`), and uploads it to
   S3-compatible storage (multipart) or a local directory. A manifest
   records the Araldo version, schema version, Postgres version, the master
   key IDs referenced, the time, and a SHA-256 of the file.
2. **The server holds only public keys.** It can write backups but never
   read them; decryption needs the matching age identity, which the
   operator keeps offline.
3. **`araldo backup list | verify | restore`:**
   - `verify` decrypts a backup (given the identity) and checks its hash
     and manifest. With `--scratch-dsn` it restores into a scratch database
     to prove the backup actually works.
   - `restore` loads a backup into an empty database, then runs any newer
     migrations. It refuses a non-empty database without `--force`.
4. **Scheduled backups** are an `opsched` task, off until configured, with
   a retention policy (for example 7 daily, 4 weekly, 6 monthly). The last
   success is exported as a metric and can ping a dead-man's switch.
5. **The container image includes `pg_dump`.** `backup create` checks that
   its major version is at least the server's and stops with a clear message
   if not.
6. **Master keys are not in the backup** ([ADR 0008](0008-encryption.md)).
   `restore` checks that the configured keyring can unwrap a data key for
   every master key ID in the manifest, and warns if not (channels would
   need reconnecting).
7. **Media:** for S3 storage, the docs recommend bucket versioning. For
   local-disk media, `--include-media` adds the files to the archive.

## Alternatives considered

- **Continuous WAL archiving and point-in-time recovery** (pgBackRest,
  WAL-G). Better recovery points and the right choice for large installs;
  the docs will point to them. Too much to build in or require.
- **A logical dump written in Go.** It would reinvent `pg_dump`, badly.
- **Encrypting with a symmetric key held by the server.** Anyone who takes
  over the server could then read every past backup.

## Consequences

- `filippo.io/age` and an S3 client (`minio-go`) join the dependencies.
- Operators manage two secrets offline: the master keys and the backup
  identity. The operations guide covers both, with a restore drill.
