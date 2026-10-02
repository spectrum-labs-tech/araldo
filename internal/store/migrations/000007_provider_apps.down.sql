ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_app_fk;
ALTER TABLE channels DROP COLUMN IF EXISTS token_expires_at;
ALTER TABLE channels DROP COLUMN IF EXISTS app_id;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS provider_apps;
