SET LOCAL lock_timeout = '5s';

-- Passkeys (ADR 0007): a user's WebAuthn credentials, the library's record
-- of each kept whole, found by its credential ID.
CREATE TABLE passkeys (
    id            uuid        NOT NULL PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id bytea       NOT NULL UNIQUE,
    credential    jsonb       NOT NULL,
    name          text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);
CREATE INDEX passkeys_user_idx ON passkeys (user_id);

-- A WebAuthn ceremony between its two halves: single use, a few minutes.
CREATE TABLE passkey_challenges (
    token_hash bytea       NOT NULL PRIMARY KEY,
    user_id    uuid        REFERENCES users (id) ON DELETE CASCADE,
    purpose    text        NOT NULL,
    session    jsonb       NOT NULL,
    expires_at timestamptz NOT NULL
);
