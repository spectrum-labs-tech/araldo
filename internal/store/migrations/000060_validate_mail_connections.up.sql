SET LOCAL lock_timeout = '5s';

-- Validates what 000058 added NOT VALID; VALIDATE lets writes go on
-- (ADR 0029).
ALTER TABLE mail_accounts VALIDATE CONSTRAINT mail_accounts_connection_fk;
