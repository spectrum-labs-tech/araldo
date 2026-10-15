-- Deleting a mail account sets its deliveries' mail_account_id to NULL
-- through (org_id, mail_account_id): without an index each delete scans
-- every delivery. Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS newsletter_deliveries_mail_account_idx ON newsletter_deliveries (org_id, mail_account_id)
    WHERE mail_account_id IS NOT NULL;
