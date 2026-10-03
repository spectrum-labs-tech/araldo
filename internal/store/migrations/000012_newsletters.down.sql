DROP TABLE IF EXISTS newsletter_media;
DROP TABLE IF EXISTS newsletter_deliveries;
DROP TABLE IF EXISTS newsletter_issues;
DROP TABLE IF EXISTS mail_accounts;
ALTER TABLE brands
    DROP CONSTRAINT IF EXISTS brands_email_logo_fk,
    DROP COLUMN IF EXISTS email_logo_media_id,
    DROP COLUMN IF EXISTS email_accent,
    DROP COLUMN IF EXISTS email_address,
    DROP COLUMN IF EXISTS email_footer;
