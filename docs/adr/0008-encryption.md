# ADR 0008: Secrets are envelope-encrypted per org; the master keys never touch the database

- Status: proposed
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
     primary), a file, or a KMS provider behind an interface (OpenBao/Vault
     Transit first, cloud KMS later).
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
   - `araldo keys rotate` adds a new primary master key and rewraps every
     data key. The secrets themselves are untouched, so it is fast.
   - `araldo keys rotate-org <org>` issues a new data key and re-encrypts
     that org's secrets.
   - An old master key can be removed once nothing references its ID.
5. **Crypto-shredding.** Deleting an org deletes its data key, so the org's
   secrets are unreadable even in old backups.
6. **Master keys are never written to the database, backups, logs or
   telemetry.** Losing them means every connected channel must reconnect,
   but posts, templates and history survive. `araldo keys export` prints
   them for offline escrow, and the operations guide says so prominently.
7. **Implementation** uses only the standard library (`crypto/aes`,
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

- Operators must provide and keep a master key; `araldo server` refuses to
  start without one.
- Encrypted columns cannot be searched or indexed; nothing needs to be.
- Tests cover: round trips, a ciphertext moved to another row failing,
  rotation keeping every secret readable, and org deletion making its
  secrets unreadable.
