SET LOCAL lock_timeout = '5s';

-- Check the rows written before 000018, letting writes go on meanwhile
-- (ADR 0029).
ALTER TABLE post_targets VALIDATE CONSTRAINT post_targets_post_mode_fkey;
ALTER TABLE post_targets VALIDATE CONSTRAINT post_targets_channel_mode_fkey;
ALTER TABLE newsletter_deliveries VALIDATE CONSTRAINT newsletter_deliveries_mail_account_mode_fkey;
