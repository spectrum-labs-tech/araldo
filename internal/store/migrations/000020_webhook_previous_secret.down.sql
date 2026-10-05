ALTER TABLE webhook_endpoints
    DROP COLUMN IF EXISTS previous_secret,
    DROP COLUMN IF EXISTS previous_secret_until;
