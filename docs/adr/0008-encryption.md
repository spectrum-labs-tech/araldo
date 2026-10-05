# ADR 0008: Secrets are envelope-encrypted per org; the master keys never touch the database

- Status: accepted; built (deleting an org crypto-shreds it), except `keys rotate-org` and `keys export`
- Date: 2026-09-28

## Context

Araldo stores credentials that can post as other people's companies: OAuth
access and refresh tokens, app passwords, developer app secrets, webhook
signing secrets, TOTP secrets. The likeliest leaks are a database dump, a
backup in the wrong bucket, or read access to the database through some
other bug. Any of those must not hand an attacker working credentials.

The usual approach, one AES-GCM key from an environment variable, is a
good start, but it has no key rotation, no way to
erase one tenant's secrets, and nothing stops a ciphertext from being copied
to another row, where it would decrypt without error.

## Decision

1. **Key hierarchy (envelope encryption):**
   - **Master keys** (key-encryption keys) come from outside the database:
     `ARALDO_MASTER_KEYS` (a list of `id:base64` 256-bit keys; the first is
     primary), a file, or a key service behind the same interface
     (OpenBao/Vault Transit now, cloud KMS later). A Transit key never
     leaves the service: Araldo sends it data keys to wrap and unwrap, and
     binds each to its scope and version by prefixing a hash of them, which
     unwrapping checks, since Transit has no associated data. While the
     master keys are unavailable (none configured, or the service down),
     Araldo keeps running: only operations that need a key fail, with a
     503 to retry.
   - **Each org has a data key** (256-bit, random), stored in the database
     wrapped by the primary master key, together with that key's ID.
   - Secrets are encrypted with the org's data key using AES-256-GCM.
     Secrets that belong to the whole install (install-wide developer apps,
     SMTP settings) use an install data key the same way.
2. **Each ciphertext is bound to its place.** The associated data is
   `araldo:v1:<table>:<column>:<row id>`, so a ciphertext copied to another
   row or column fails to decrypt. The stored format is a version byte, the
   data key version, the nonce, then the ciphertext.
3. **What is protected, and how:**

   | Data | Protection |
   |---|---|
   | OAuth tokens, app passwords, developer app secrets, webhook signing secrets, TOTP secrets, SMTP/email credentials | encrypted (needed again in plaintext) |
   | passwords | argon2id |
   | API keys, session tokens, recovery codes (80 random bits), email and reset tokens | SHA-256 (random, high entropy) |
   | posts, templates, media, names, emails | not encrypted by Araldo; disk encryption and TLS to Postgres are the operator's job |

4. **Rotation:**
   - `araldo admin keys rotate` adds a new primary master key and rewraps every
     data key. The secrets themselves are untouched, so it is fast.
   - `araldo admin keys rotate-org <org>` issues a new data key and re-encrypts
     that org's secrets.
   - An old master key can be removed once nothing references its ID.
5. **Crypto-shredding.** Deleting an org deletes its data key, so the org's
   secrets are unreadable even in old backups.
6. **Signing keys are stored, not derived from a master key.** Keys for
   signing (media links) come from one data key in a reserved scope,
   wrapped like any other, so changing master keys never invalidates a
   link already sent, such as a newsletter's images. On first start it is
   seeded with the value earlier versions derived from the primary local
   key, so their links stay valid too.
7. **Master keys are never written to the database, backups, logs or
   telemetry.** Losing them means every connected channel must reconnect,
   but posts, templates and history survive. `araldo admin keys export` prints
   them for offline escrow, and the operations guide says so prominently.
8. **Implementation** uses only the standard library (`crypto/aes`,
   `crypto/cipher`, `crypto/rand`) in `internal/keyring`. Decrypted data
   keys are cached in memory briefly. Nothing is invented: this is standard
   AEAD plus key wrapping.

## Alternatives considered

- **One key for everything.** No rotation, no per-tenant
  erasure, no protection against copied ciphertexts.
- **pgcrypto.** Keys would travel to the database server and could appear
  in its logs and statement statistics.
- **Google Tink.** Well designed, but a large dependency for what is two
  small functions plus a keyring.

## Consequences

- Operators must provide and keep a master key. Without one, `araldo server`
  still starts, and what needs a secret fails until a key is available
  (decision 1).
- Encrypted columns cannot be searched or indexed; nothing needs to be.
- Tests cover: round trips, a ciphertext moved to another row failing,
  rotation keeping every secret readable, and org deletion making its
  secrets unreadable.
