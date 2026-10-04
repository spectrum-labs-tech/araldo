# ADR 0007: Passwords, passkeys and TOTP built in; MFA can be required per org

- Status: proposed
- Date: 2026-09-28

## Context

A dashboard account can connect social accounts and create API keys that
post as a company. Stolen credentials here mean public damage. Self-hosters
may have no identity provider at all, so sign-in with strong MFA must work
out of the box; single sign-on is an addition, not a requirement.

## Decision

1. **Sign-in methods:**
   - **Email and password.** Passwords are hashed with argon2id, with the
     parameters stored alongside each hash so they can be raised later.
     Passwords must be 12–128 characters, with no composition rules. An
     optional breached-password check uses the Have I Been Pwned range API
     (k-anonymity); it is off by default so air-gapped installs work.
   - **Passkeys (WebAuthn)**, via `go-webauthn/webauthn`. A passkey can be
     the only sign-in method and counts as multi-factor.
2. **Second factors:**
   - **TOTP** (RFC 6238, 6 digits, 30 seconds, one step of clock drift).
     The secret is encrypted ([ADR 0008](0008-encryption.md)), and the last
     used time step is stored so a code cannot be replayed.
   - **Recovery codes:** ten single-use codes of 80 random bits each,
     stored as SHA-256 hashes. Generating new codes invalidates the old set.
3. **Org policy `require_mfa`.** A member without a second factor (or a
   passkey) can still sign in but cannot see that org's data until they
   enroll.
4. **Sessions:**
   - A random 256-bit token in a `__Host-` cookie (`Secure`, `HttpOnly`,
     `SameSite=Lax`), stored as a hash.
   - Sessions expire after 7 days idle or 30 days at most, and can be
     listed and revoked.
   - **Sudo mode:** sensitive actions (creating API keys, changing MFA,
     removing owners, deleting an org) require re-authenticating within the
     last 10 minutes.
5. **Forms** use a per-session CSRF token as well as `SameSite=Lax`.
6. **Brute force and enumeration:**
   - sign-in is rate limited per account and per IP, backing off without
     permanent lockout;
   - sign-in, sign-up and password reset give the same response whether or
     not the email exists.
7. **Email flows** (verification, password reset with a one-hour
   single-use token, "new sign-in" notices) need SMTP settings. Without them,
   operators reset passwords with `araldo users reset-password`.
8. **Bootstrap:** `araldo users create --email … --owner-of "Org name"`
   creates the first account. There is no default admin password.
9. **Single sign-on** (OIDC) and SCIM provisioning come later, each with its
   own ADR.

## Alternatives considered

- **OIDC only** (caseline). Right for health departments that all run an
  identity provider; wrong for a developer self-hosting on a small server.
- **An external auth service** (Ory, Keycloak, Authentik). Another service
  to run and upgrade, and one more thing between "download" and "working".
- **SMS codes.** Weak against SIM swapping, and they need a paid provider.

## Consequences

- Araldo owns security-critical code: password hashing, WebAuthn
  ceremonies, TOTP, sessions. Each is covered by table-driven tests,
  including replay and expiry, and uses established libraries where one
  exists.
- `go-webauthn/webauthn` and `golang.org/x/crypto` (for argon2) join the
  dependencies.
