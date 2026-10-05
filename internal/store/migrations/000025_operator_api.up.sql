SET LOCAL lock_timeout = '5s';

-- Org limits and status, and an external reference (ADR 0031). A limit
-- left NULL is unlimited.
ALTER TABLE orgs ADD COLUMN status text NOT NULL DEFAULT 'active';
ALTER TABLE orgs ADD COLUMN status_note text NOT NULL DEFAULT '';
ALTER TABLE orgs ADD COLUMN external_ref text;
ALTER TABLE orgs ADD COLUMN limit_brands integer;
ALTER TABLE orgs ADD COLUMN limit_channels integer;
ALTER TABLE orgs ADD COLUMN limit_members integer;
ALTER TABLE orgs ADD COLUMN limit_posts_month integer;
ALTER TABLE orgs ADD CONSTRAINT orgs_status_check CHECK (status IN ('active', 'read_only', 'suspended')) NOT VALID;
ALTER TABLE orgs ADD CONSTRAINT orgs_limits_check CHECK (limit_brands >= 0 AND limit_channels >= 0 AND limit_members >= 0
    AND limit_posts_month >= 0) NOT VALID;

-- Keys for the operator API: install-wide, in no org.
CREATE TABLE operator_keys (
    id           uuid        NOT NULL PRIMARY KEY,
    name         text        NOT NULL,
    key_hash     bytea       NOT NULL UNIQUE,
    hint         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
