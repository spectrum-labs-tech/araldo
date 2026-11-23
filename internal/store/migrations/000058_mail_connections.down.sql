ALTER TABLE mail_accounts DROP CONSTRAINT IF EXISTS mail_accounts_connection_fk;
ALTER TABLE mail_accounts DROP COLUMN IF EXISTS connection_id;
DROP TABLE IF EXISTS mail_connections;
