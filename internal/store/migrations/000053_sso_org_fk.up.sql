SET LOCAL lock_timeout = '5s';

-- The org a session, CLI token or device sign-in came through by single
-- sign-on (000032) had no foreign key, so deleting the org left them
-- pointing at nothing. Such a credential reaches that org only (ADR 0033),
-- so it goes with the org: setting it NULL instead would widen it to every
-- org of its person. NOT VALID, so adding it scans nothing; 000054 removes
-- what earlier deletes orphaned and validates it (ADR 0029).
ALTER TABLE sessions ADD CONSTRAINT sessions_sso_org_fk
    FOREIGN KEY (sso_org_id) REFERENCES orgs (id) ON DELETE CASCADE NOT VALID;
ALTER TABLE user_tokens ADD CONSTRAINT user_tokens_sso_org_fk
    FOREIGN KEY (sso_org_id) REFERENCES orgs (id) ON DELETE CASCADE NOT VALID;
ALTER TABLE device_authorizations ADD CONSTRAINT device_authorizations_sso_org_fk
    FOREIGN KEY (sso_org_id) REFERENCES orgs (id) ON DELETE CASCADE NOT VALID;
