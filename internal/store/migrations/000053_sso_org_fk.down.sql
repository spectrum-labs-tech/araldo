SET LOCAL lock_timeout = '5s';

ALTER TABLE IF EXISTS device_authorizations DROP CONSTRAINT IF EXISTS device_authorizations_sso_org_fk;
ALTER TABLE IF EXISTS user_tokens DROP CONSTRAINT IF EXISTS user_tokens_sso_org_fk;
ALTER TABLE IF EXISTS sessions DROP CONSTRAINT IF EXISTS sessions_sso_org_fk;
