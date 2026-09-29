# Araldo

Araldo (Italian for *herald*) is a developer-first distribution API: your
product sends it an announcement, and Araldo renders it for every social
platform, schedules it, publishes it, and tells you (by webhook) where it
landed.

```sh
curl https://araldo.example/v1/posts \
  -H "Authorization: Bearer ald_test_…" \
  -H "Idempotency-Key: build-8812" \
  -d '{"brand":"brand_…","template":"featured-build","data":{"name":"Recce","url":"https://ar15.build/b/8812"},"publish_at":"next_slot"}'
```

**Status: pre-alpha.** It runs, publishes, and is used by Spectrum Labs'
own products, but the API may still change.

## What it does

- **Templates** with a JSON Schema for their data and a body per platform,
  checked against each platform's real rules (length and how it is
  counted, threads, media) before anything is scheduled.
- **Preview** any post without side effects: every rule violation comes
  back with a stable code, so an AI agent in your product can fix its own
  text.
- **Scheduling** at a time, now, or in the brand's next free weekly slot.
- **Publishing** that never double-posts silently: uncertain attempts are
  retried only on platforms that make retries safe, and otherwise wait for
  a person.
- **Test mode** with sandbox channels that follow each platform's rules and
  can simulate failures, so you can integrate before any platform approves
  anything.
- **Signed webhooks** with retries and a delivery log.
- **Multi-tenant**: orgs, brands, members with roles, optional approvals,
  MFA, scoped API keys, an audit log. Secrets are envelope-encrypted per
  org.

Platforms today: Bluesky, Mastodon, Discord (webhooks), Telegram (bots),
plus the sandbox. X, Facebook, Instagram, Threads and LinkedIn are next
([roadmap](docs/roadmap.md)).

## Run it

You need Go (version in `go.mod`), [Task](https://taskfile.dev) and Docker.

```sh
task db:up            # Postgres for development
task run              # server + worker on http://localhost:8080
task bootstrap -- --email you@example.com --org "Your org"   # first owner
```

`araldo keys generate` prints a master key for production; see
[docs/operations.md](docs/operations.md).

## Test

```sh
task check                 # fmt, vet, lint, unit tests: no database needed
task db:up && task test:integration
```

## Learn more

- [Architecture](docs/architecture.md) and the [decision records](docs/adr/)
- [API reference](api/openapi.yaml)
- [Roadmap](docs/roadmap.md)

## License

The server is [AGPL-3.0-or-later](LICENSE). The API contract and client
SDKs are Apache-2.0 ([ADR 0003](docs/adr/0003-license.md)).
