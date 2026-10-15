SET LOCAL lock_timeout = '5s';

-- Who holds a claimed newsletter hand-off or email, so a worker that
-- outlived its lease cannot record an outcome over the worker that took the
-- row next, and send twice. NULL while no one holds it.
ALTER TABLE newsletter_deliveries ADD COLUMN lease_owner text;
ALTER TABLE notification_emails ADD COLUMN lease_owner text;
ALTER TABLE report_emails ADD COLUMN lease_owner text;
