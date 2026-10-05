SET LOCAL lock_timeout = '5s';

-- Test mode never reaches a real account (ADR 0006), and the database now
-- holds to it, not only the code: a target belongs to a post and a channel
-- of its own mode, and a newsletter delivery to a mail account of its own.
-- Added NOT VALID, without scanning; 000019 validates them (ADR 0029).
ALTER TABLE post_targets ADD CONSTRAINT post_targets_post_mode_fkey
    FOREIGN KEY (org_id, post_id, livemode) REFERENCES posts (org_id, id, livemode) ON DELETE CASCADE NOT VALID;
ALTER TABLE post_targets ADD CONSTRAINT post_targets_channel_mode_fkey
    FOREIGN KEY (org_id, channel_id, livemode) REFERENCES channels (org_id, id, livemode) ON DELETE CASCADE NOT VALID;
ALTER TABLE newsletter_deliveries ADD CONSTRAINT newsletter_deliveries_mail_account_mode_fkey
    FOREIGN KEY (org_id, mail_account_id, livemode) REFERENCES mail_accounts (org_id, id, livemode)
    ON DELETE SET NULL (mail_account_id) NOT VALID;
