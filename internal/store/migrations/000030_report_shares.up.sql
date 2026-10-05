SET LOCAL lock_timeout = '5s';

-- Links to a brand's monthly report for people outside the org (ADR 0026),
-- by a token kept only as its hash.
CREATE TABLE report_shares (
    id         uuid        NOT NULL PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    brand_id   uuid        NOT NULL,
    livemode   boolean     NOT NULL,
    month      text        NOT NULL,
    token_hash bytea       NOT NULL UNIQUE,
    created_by uuid,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);
CREATE INDEX report_shares_brand_idx ON report_shares (org_id, brand_id);
