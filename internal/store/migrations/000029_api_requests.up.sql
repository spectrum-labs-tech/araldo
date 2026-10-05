SET LOCAL lock_timeout = '5s';

-- The request log (ADR 0032): each authenticated API request, briefly, for
-- the org's developers to see what their integration sent and got back.
-- No bodies or query strings are kept.
CREATE TABLE api_requests (
    id            uuid        NOT NULL PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    livemode      boolean     NOT NULL,
    method        text        NOT NULL,
    route         text        NOT NULL,
    path          text        NOT NULL,
    status        integer     NOT NULL,
    error_code    text        NOT NULL DEFAULT '',
    duration_ms   integer     NOT NULL,
    key_id        uuid,
    user_token_id uuid,
    request_id    text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_requests_list_idx ON api_requests (org_id, livemode, id DESC);
