SET LOCAL lock_timeout = '5s';

ALTER TABLE user_tokens DROP COLUMN sso_org_id;
ALTER TABLE device_authorizations DROP COLUMN sso_org_id;
ALTER TABLE sessions DROP COLUMN sso_org_id;
ALTER TABLE orgs DROP COLUMN require_sso;
DROP TABLE sso_states;
DROP TABLE sso_domains;
DROP TABLE sso_connections;
