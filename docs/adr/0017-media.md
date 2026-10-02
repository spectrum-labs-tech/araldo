# ADR 0017: Images are uploaded once, checked against every platform's rules, and stored in Postgres unless S3 is configured

- Status: proposed
- Date: 2026-10-02

## Context

Most social posts carry an image, and Instagram takes none without one.
Each platform accepts different formats, sizes and shapes: Bluesky takes
images up to 1,000,000 bytes, Mastodon up to 16 MiB, Instagram only JPEGs
between 4:5 and 1.91:1. A post that breaks one of these fails at publish
time, hours after whoever scheduled it has moved on.

[ADR 0009](0009-platform-adapters.md) put media in S3-compatible storage,
with local disk in development. Local disk does not work once the server
(which receives an upload) and the worker (which publishes it) run as
separate processes, which is how the Helm chart deploys them, and requiring
S3 breaks "one binary and a Postgres" ([ADR 0002](0002-layout-and-storage.md)).

## Decision

1. **Media is an object** (`media_…`) that belongs to a brand and a mode,
   like a post ([ADR 0004](0004-tenancy-roles-approvals.md),
   [ADR 0006](0006-test-mode-and-api-keys.md)). `POST /v1/media` creates
   one from a `multipart/form-data` upload (`file`), or from a `url` that
   Araldo fetches through the same guard as webhooks, refusing private
   addresses ([ADR 0012](0012-events-and-webhooks.md)). Either way the
   file is at most 16 MiB (the most any supported platform accepts).
   Media needs the `posts:write` scope to create and `posts:read` to read,
   since it exists only to be posted.
2. **Version 1 is images**: JPEG, PNG, GIF and WebP. The type comes from
   the file's bytes, never from a file name or a `Content-Type` header,
   and the dimensions from its header. Araldo never decodes the pixels, so
   a decompression bomb costs it nothing. Video, resizing and transcoding
   come later.
3. **The file is immutable**; the `alt` text can be changed. Alt text is at
   most 1,000 characters (X's limit, the lowest of the platforms that take
   it), and it is sent to every platform that accepts it.
4. **A post lists up to ten media IDs** (`media`). Each channel's rules
   check them when the post is created or previewed, as they check text:
   the type, size in bytes, aspect ratio and count, each from the
   platform's own documentation and cited in `rules.go`. A violation
   refuses the post and the preview says why, per channel. Where a
   platform takes less text alongside media (Telegram's 1,024-character
   caption, against 4,096 for a message), the text limit changes too.
5. **Media goes on the first part of a thread.** A retry that resumes a
   thread after its first part does not upload it again.
6. **Storage is Postgres by default**: the bytes go in a `media_blobs`
   table, in the same transaction as the media row. Every process already
   shares the database, so server and worker need nothing more, and
   database backups include media. Setting `ARALDO_S3_BUCKET` (with an
   endpoint and keys) stores new files in S3-compatible storage instead.
   Each row records where its file lives, so files stored before a change
   stay readable. The S3 client signs requests itself (Signature
   Version 4) rather than pulling in an SDK.
7. **A media object that no post uses is deleted after 24 hours**, by an
   `opsched` task. One that a post uses cannot be deleted (409
   `media_in_use`) and is kept as long as the post is.

## Alternatives considered

- **Media URLs on the post**, fetched at publish time. Nothing could be
  checked when the post is created, the image could change or vanish
  between preview and publishing, and every retry would fetch it again.
- **S3 required.** Simpler code, but every self-hoster would need a bucket
  before posting a single image.
- **Local disk.** Fine for one process, wrong as soon as there are two.
- **Resizing to fit each platform.** Kinder, but it needs an image library
  outside the standard library to do well, and changes what the author
  chose. Refusing with a clear reason comes first.

## Consequences

- An install posting many large images grows its database by that much;
  such installs should configure S3. Files in S3 are not in database
  backups and need their own.
- Uploads make some requests up to 16 MiB, so the body limit for
  `POST /v1/media` and the dashboard's post form is raised above 1 MiB.
- Each adapter learns its platform's upload API (Bluesky blobs, Mastodon
  media attachments, Discord and Telegram multipart uploads), with tests
  against fake servers as before.
