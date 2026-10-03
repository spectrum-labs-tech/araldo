# ADR 0021: Channels connect with OAuth through an org's developer apps; images reach platforms by signed links

- Status: proposed
- Date: 2026-10-02

## Context

[ADR 0009](0009-platform-adapters.md) put developer app credentials in the
database and called for OAuth connect flows. So far every adapter takes
credentials a person pastes: an app password (Bluesky), a token (Mastodon,
LinkedIn), keys (X). Meta's platforms (Threads, Instagram, Facebook Pages)
are only practical with OAuth: their tokens come from a sign-in, expire,
and must be refreshed. Instagram and Threads also take images only by
URL: they fetch the file themselves.

## Decision

1. **Developer apps belong to an org** (`provider_apps`): a provider, a
   name, the client ID, and the client secret, encrypted like channel
   credentials ([ADR 0008](0008-encryption.md)). Admins manage them in the
   dashboard. Install-wide apps, shared by every org, come later.
2. **Connecting is a browser flow** in the dashboard, in live mode:
   *Connect with Threads* → choose the app (when there is more than one)
   → the platform's sign-in → `/connect/{provider}/callback` → choose which
   of the accounts it returned to connect (a member may manage several
   Facebook Pages) → channels. The `state` parameter is a random value
   whose hash is stored with the org, brand, member and app for 15
   minutes, and used once; PKCE is used where the platform supports it.
   The redirect URI to register with the platform is
   `{ARALDO_BASE_URL}/connect/{provider}/callback`.
3. **Adapters opt in** with `Connector` (authorize URL, code exchange,
   returning the connectable accounts) and `Refresher` (renew a token, with
   the app's credentials). A channel remembers its app and when its token
   expires. An adapter can take a sign-in and pasted credentials both (X,
   LinkedIn); a pasted channel has no app and is never renewed.
4. **Tokens are renewed before they expire**: an `opsched` task
   (`channels.refresh`, hourly) refreshes those expiring within 7 days. A
   refresh that fails as revoked marks the channel `needs_reauth` and emits
   `channel.needs_reauth`; reconnecting is the same flow again.
   - A renewal holds the channel's row lock, and does nothing if the token
     changed while it waited: some platforms (X) replace the refresh token
     on every renewal, so two renewals racing with the same one would read
     as revoked.
   - Publishing and reading engagement renew a token first when it expires
     within five minutes, in case the hourly task fell behind (an X token
     lasts two hours).
   - A token the platform gives no way to renew (a LinkedIn token without a
     refresh token) keeps working until it expires: the channel says when
     to sign in again, and needs it once the token has expired.
5. **Images reach platforms by signed links**:
   `GET /v1/media/{id}/content?expires=…&signature=…` serves a file without
   an API key when the signature is an HMAC of the ID and expiry under a
   key derived from the primary master key, and the expiry has not passed
   (links last an hour). The API is the public part of an install, so the
   platform can fetch from it; adapters get the link in the payload.
   Changing the primary master key invalidates old links, which is fine for
   links this short.

## Alternatives considered

- **Install-wide apps only**, from environment variables (ar15.build's
  way). One app per install, no self-service, and every org shares one
  app's quota and standing.
- **Public media without signatures** (unguessable IDs alone). A leaked ID
  would serve the file forever; signed links expire.
- **A third-party storage URL** for every image (S3 presigned). Only works
  with S3 configured; signed links work with Postgres storage too.

## Consequences

- An install whose API is not reachable from the internet cannot post
  images to Instagram or Threads.
- Each OAuth platform needs its app registered with the platform (and,
  for wider use, the platform's review); the operations guide walks
  through it per platform.
- X and LinkedIn can move to this flow later and keep working with pasted
  credentials meanwhile.
