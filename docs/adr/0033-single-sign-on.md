# ADR 0033: OIDC single sign-on, by verified domain

- Status: accepted; built
- Date: 2026-10-05
- Builds on: [ADR 0004](0004-tenancy-roles-approvals.md), [ADR 0007](0007-authentication-and-mfa.md), [ADR 0008](0008-encryption.md), [ADR 0028](0028-cli-as-api-client.md)

## Context

Teams that already run an identity provider (Okta, Entra ID, Google
Workspace, Keycloak…) want their people to sign in with it, to join
without an invitation, and to lose access when they leave the company. An
org owner should be able to set this up themselves, without the operator.

## Decision

1. **One OpenID Connect provider per org.** An owner, in sudo mode, enters
   the issuer, client ID and client secret; Araldo reads the issuer's
   discovery document to check it, and seals the secret with the org's
   data key. The redirect URI to register is `<base URL>/login/sso/callback`.
   Providers are reached through the same network guard as webhooks.
2. **Domains are proven by DNS.** An owner adds an email domain and
   publishes a TXT record at `_araldo-verify.<domain>` holding
   `araldo-verify=<token>`; Araldo looks for it when asked. A domain is
   verified for one org at most, so an email address leads to one
   provider.
3. **Sign-in starts from the email address.** Its domain names the org;
   the person goes to its provider with the authorization code flow, PKCE
   and a nonce, and a state bound to their browser by a cookie and used
   once. The ID token must come from the issuer, for the client, unexpired,
   with the nonce, and carry an email at one of the org's verified domains;
   one the provider says is unverified (`email_verified: false`) is
   refused.
4. **People join automatically.** Someone new gets an account without a
   password; someone not yet a member joins with the org's default role
   (admin, editor or viewer, never owner), within its member limit. Roles
   are managed in Araldo after that.
5. **The session needs no Araldo second factor**: the provider is the
   org's, with its own policy (it satisfies the org's two-factor
   requirement too). It records that it came through that org's single
   sign-on, and **reaches that org only**: the provider vouches for the
   person there, not for their account, so the session cannot switch to
   their other orgs or change how they sign in (password, authenticator,
   passkeys), and a CLI token approved from it works in that org only.
   Someone who belongs to other orgs signs in to those as before.
6. **An org can require it.** Then its dashboard is reachable only from a
   session that came through its single sign-on, and CLI tokens work in it
   only when approved from one. An owner turns it on only from such a
   session (so they know it works), and off in sudo mode. While it is on,
   the provider and the last verified domain cannot be removed. The
   operator can turn it off (`araldo admin org update --require-sso
   false`) when an org's provider is gone. API keys belong to the org and
   are unaffected.

## Consequences

- An org can onboard a team without invitations, and with "require",
  off-boarding happens in its provider: CLI tokens and sessions made
  another way stop working in the org.
- Whoever controls the provider can sign in as anyone at the org's
  verified domains, so connecting one is an owner's action in sudo mode,
  and verification proves control of the domain first.
- Leaving the provider does not end a session already open, which lasts
  as any session does (ADR 0007). There is no SCIM: someone removed in
  Araldo who can still sign in at the provider joins again with the
  default role, so people leave by leaving the provider (or the app
  assignment there).
- SAML is not supported; the common providers all speak OpenID Connect.
