SET LOCAL lock_timeout = '5s';

-- Developer apps the install provides to every org (ADR 0030). The secret
-- is sealed with the install data key (ADR 0008).
CREATE TABLE install_apps (
    id            uuid        NOT NULL PRIMARY KEY,
    provider      text        NOT NULL,
    name          text        NOT NULL,
    client_id     text        NOT NULL,
    client_secret bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, name)
);

-- What was connected through one, beside app_id for an org's own apps.
ALTER TABLE channels ADD COLUMN install_app_id uuid REFERENCES install_apps (id) ON DELETE SET NULL;
ALTER TABLE ad_accounts ADD COLUMN install_app_id uuid REFERENCES install_apps (id) ON DELETE SET NULL;
ALTER TABLE oauth_states ADD COLUMN install_app_id uuid REFERENCES install_apps (id) ON DELETE CASCADE;
-- A sign-in names an org's app or an install's; app_id stops being required
-- for it, which the release before, writing app_id always, never notices.
ALTER TABLE oauth_states ALTER COLUMN app_id DROP NOT NULL;
ALTER TABLE channels ADD CONSTRAINT channels_one_app_check CHECK (app_id IS NULL OR install_app_id IS NULL) NOT VALID;
ALTER TABLE ad_accounts ADD CONSTRAINT ad_accounts_one_app_check CHECK (app_id IS NULL OR install_app_id IS NULL) NOT VALID;
ALTER TABLE oauth_states ADD CONSTRAINT oauth_states_one_app_check CHECK ((app_id IS NULL) <> (install_app_id IS NULL)) NOT VALID;
