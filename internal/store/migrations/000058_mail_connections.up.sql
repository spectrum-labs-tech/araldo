SET LOCAL lock_timeout = '5s';

-- Mail connections (ADR 0024 decision 3, amended): one provider account's
-- credentials, which any number of senders (mail_accounts) use. Org-wide,
-- since one provider account sends for several brands. legacy_aad marks
-- credentials still sealed for the sender a connection was adopted from
-- (its ID is that sender's), until the key is next replaced.
CREATE TABLE mail_connections (
    id          uuid        NOT NULL PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    livemode    boolean     NOT NULL,
    provider    text        NOT NULL,
    name        text        NOT NULL,
    settings    jsonb       NOT NULL DEFAULT '{}',
    credentials bytea,
    legacy_aad  boolean     NOT NULL DEFAULT false,
    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_reauth')),
    status_note text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mail_connections_mode_key ON mail_connections (org_id, id, livemode);

-- A sender's connection, of its own mode. Nullable while the previous
-- release, which knows no connections, may still be connecting senders:
-- the new one adopts those on first read. NOT VALID, so adding it scans
-- nothing; 000060 validates it (ADR 0029).
ALTER TABLE mail_accounts ADD COLUMN connection_id uuid;
ALTER TABLE mail_accounts ADD CONSTRAINT mail_accounts_connection_fk
    FOREIGN KEY (org_id, connection_id, livemode) REFERENCES mail_connections (org_id, id, livemode) NOT VALID;
