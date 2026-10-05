# ADR 0030: Install-wide developer apps, offered to every org beside its own

- Status: accepted; built
- Date: 2026-10-05
- Builds on: [ADR 0021](0021-oauth-connections.md) ("install-wide apps come later"), [ADR 0008](0008-encryption.md) (the install data key)

## Context

Signing in to X, LinkedIn, Meta, Pinterest, Google, TikTok or Reddit goes
through a developer app the org registers with the platform
([ADR 0021](0021-oauth-connections.md)). For a self-hoster that is one
afternoon. For a hosted Araldo it is a wall: every customer would have to
register an app on every platform, and for most of them wait out the
platform's review, before posting anything. A hosted plan
([roadmap](../roadmap.md) item 14) needs apps the install registers once,
that every org can sign in through.

An org's apps are bound to the org in the schema: channels, ad accounts
and sign-ins reference `(org_id, app_id)`, so an app cannot be shared by
changing who owns a row.

## Decision

1. **Install-wide apps are their own table**, `install_apps`: provider,
   name, client ID, and the client secret sealed with the install data key
   (`keyring.Install`), bound to its row like every secret.
2. **Channels, ad accounts and sign-ins name one app or the other**: a new
   nullable `install_app_id` beside `app_id`, and a check that a row never
   names both (a sign-in names exactly one).
3. **The operator manages them**, as server administration
   ([ADR 0028](0028-cli-as-api-client.md)): `araldo admin apps list | add |
   rename | remove`, audited as the operator. Members never see their
   secrets or change them; the dashboard lists them as provided by the
   server.
4. **Every org can sign in through them, beside its own apps.** Connecting
   offers the org's apps first, then the install's: an org that wants its
   own quota and standing with a platform registers its own app and it is
   used by default.
5. **Removing an install app** leaves the channels made through it working
   until their tokens need renewing, as removing an org's app does; then
   they sign in again through another app.

## Alternatives considered

- **Install apps as rows of `provider_apps` with no org.** One table, but
  every `(org_id, app_id)` foreign key would have to go, and with it the
  schema's guarantee that an org never uses another org's app.
- **Install apps from environment variables.** No new table, but one app
  per platform, changes need a restart, and secrets sit in the
  deployment's environment rather than sealed in the database like the
  rest. ADR 0021 rejected this as the only way; as the operator's way it
  loses to a command that works like the others.
- **Copying the install app into each org.** Simple to read, but a secret
  rotated once would have to be rotated in every org.

## Consequences

- A hosted install registers each platform's app once, and its customers
  connect accounts with no developer setup.
- Every org on an install shares an install app's quota and its standing
  with the platform: one org's abuse can get the app restricted for all.
  The operator watches for that, and orgs can always bring their own.
- Code that loads a channel's app checks both columns; the store's
  queries carry both.
