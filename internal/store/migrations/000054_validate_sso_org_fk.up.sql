SET LOCAL lock_timeout = '5s';

-- Credentials that came through the single sign-on of an org deleted
-- before 000053 point at nothing and reach nothing: delete them, then
-- validate the keys 000053 added. Since 000053 no new ones can appear, so
-- this touches only those few rows; VALIDATE lets writes go on (ADR 0029).
DELETE FROM sessions s WHERE sso_org_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = s.sso_org_id);
DELETE FROM user_tokens t WHERE sso_org_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = t.sso_org_id);
DELETE FROM device_authorizations d WHERE sso_org_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = d.sso_org_id);
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_sso_org_fk;
ALTER TABLE user_tokens VALIDATE CONSTRAINT user_tokens_sso_org_fk;
ALTER TABLE device_authorizations VALIDATE CONSTRAINT device_authorizations_sso_org_fk;
