DROP INDEX IF EXISTS channels_check_idx;
ALTER TABLE channels DROP COLUMN IF EXISTS check_error;
ALTER TABLE channels DROP COLUMN IF EXISTS checked_at;
