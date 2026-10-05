SET LOCAL lock_timeout = '5s';

-- Check the rows written before 000025, letting writes go on meanwhile
-- (ADR 0029).
ALTER TABLE orgs VALIDATE CONSTRAINT orgs_status_check;
ALTER TABLE orgs VALIDATE CONSTRAINT orgs_limits_check;
