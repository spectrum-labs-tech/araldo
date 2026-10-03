-- A daily health check per channel: its credentials are verified with the
-- platform, so a revoked token or a retired API shows before a post fails.
ALTER TABLE channels ADD COLUMN checked_at timestamptz;
ALTER TABLE channels ADD COLUMN check_error text NOT NULL DEFAULT '';
CREATE INDEX channels_check_idx ON channels (checked_at NULLS FIRST) WHERE status = 'active';
