SET LOCAL lock_timeout = '5s';

ALTER TABLE IF EXISTS report_emails DROP COLUMN IF EXISTS lease_owner;
ALTER TABLE IF EXISTS notification_emails DROP COLUMN IF EXISTS lease_owner;
ALTER TABLE IF EXISTS newsletter_deliveries DROP COLUMN IF EXISTS lease_owner;
