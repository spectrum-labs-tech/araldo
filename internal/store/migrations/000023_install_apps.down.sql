ALTER TABLE oauth_states DROP CONSTRAINT IF EXISTS oauth_states_one_app_check;
ALTER TABLE ad_accounts DROP CONSTRAINT IF EXISTS ad_accounts_one_app_check;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_one_app_check;
DELETE FROM oauth_states WHERE app_id IS NULL;
ALTER TABLE oauth_states ALTER COLUMN app_id SET NOT NULL;
ALTER TABLE oauth_states DROP COLUMN IF EXISTS install_app_id;
ALTER TABLE ad_accounts DROP COLUMN IF EXISTS install_app_id;
ALTER TABLE channels DROP COLUMN IF EXISTS install_app_id;
DROP TABLE IF EXISTS install_apps;
