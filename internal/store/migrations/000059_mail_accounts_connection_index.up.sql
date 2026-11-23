-- Finds a connection's senders (rotating its key, refusing its delete).
-- Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS mail_accounts_connection ON mail_accounts (org_id, connection_id);
