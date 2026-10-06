SET LOCAL lock_timeout = '5s';

-- Password resets by email (ADR 0034): a link's token, stored hashed and
-- used once.
CREATE TABLE password_resets (
    token_hash bytea       NOT NULL PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    ip         text        NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id, created_at);
