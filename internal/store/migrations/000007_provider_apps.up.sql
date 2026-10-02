-- Developer apps and OAuth connections (ADR 0021).
CREATE TABLE provider_apps (
    id            uuid        NOT NULL PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider      text        NOT NULL,
    name          text        NOT NULL,
    client_id     text        NOT NULL,
    -- Encrypted (ADR 0008).
    client_secret bytea       NOT NULL,
    created_by    uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, provider, name)
);

-- A sign-in in progress: found by the hash of its state parameter, used
-- once, kept 15 minutes. When the platform returned several accounts, the
-- encrypted connections wait here for the member to choose.
CREATE TABLE oauth_states (
    state_hash  bytea       NOT NULL PRIMARY KEY,
    org_id      uuid        NOT NULL,
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    brand_id    uuid        NOT NULL,
    app_id      uuid        NOT NULL,
    provider    text        NOT NULL,
    verifier    text        NOT NULL DEFAULT '',
    connections bytea,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, app_id) REFERENCES provider_apps (org_id, id) ON DELETE CASCADE
);

-- A channel connected through an app remembers it, and when its token
-- expires (for refreshing).
ALTER TABLE channels ADD COLUMN app_id uuid;
ALTER TABLE channels ADD COLUMN token_expires_at timestamptz;
ALTER TABLE channels ADD CONSTRAINT channels_app_fk FOREIGN KEY (org_id, app_id)
    REFERENCES provider_apps (org_id, id) ON DELETE SET NULL (app_id);
