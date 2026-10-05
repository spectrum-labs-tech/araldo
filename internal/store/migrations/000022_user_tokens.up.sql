SET LOCAL lock_timeout = '5s';

-- A person's own credential for the CLI (ADR 0028): bound to one mode, not
-- to an org (each request names the org), valid until revoked or a year
-- unused. Only its hash is kept.
CREATE TABLE user_tokens (
    id           uuid        NOT NULL PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    livemode     boolean     NOT NULL,
    name         text        NOT NULL,
    token_hash   bytea       NOT NULL UNIQUE,
    hint         text        NOT NULL,
    created_ip   text        NOT NULL DEFAULT '',
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_tokens_user_idx ON user_tokens (user_id, created_at DESC);

-- The device authorization grant (RFC 8628) that issues them: the CLI
-- holds the device code (hashed here), the person types the user code into
-- the dashboard, signed in, and approves.
CREATE TABLE device_authorizations (
    id               uuid        NOT NULL PRIMARY KEY,
    device_code_hash bytea       NOT NULL UNIQUE,
    user_code        text        NOT NULL,
    device_name      text        NOT NULL,
    livemode         boolean     NOT NULL,
    client_ip        text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'issued')),
    user_id          uuid        REFERENCES users (id) ON DELETE CASCADE,
    poll_interval    integer     NOT NULL,
    last_polled_at   timestamptz,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);
-- A user code names one authorization while it is waiting.
CREATE UNIQUE INDEX device_authorizations_code_idx ON device_authorizations (user_code) WHERE status = 'pending';
