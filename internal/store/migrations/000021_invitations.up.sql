SET LOCAL lock_timeout = '5s';

-- A person joins an org by accepting an invitation: a one-time link an
-- admin shares, valid for a week. Inviting reveals nothing about whether
-- the email has an account, and nobody joins an org without accepting.
CREATE TABLE invitations (
    id               uuid        NOT NULL PRIMARY KEY,
    org_id           uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    email            text        NOT NULL,
    email_normalized text        NOT NULL,
    role             text        NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    token_hash       bytea       NOT NULL UNIQUE,
    invited_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    expires_at       timestamptz NOT NULL,
    accepted_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id)
);
-- One open invitation per person per org: inviting again replaces it.
CREATE UNIQUE INDEX invitations_open_idx ON invitations (org_id, email_normalized) WHERE accepted_at IS NULL;
