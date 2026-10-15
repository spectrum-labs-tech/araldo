SET LOCAL lock_timeout = '5s';

ALTER TABLE IF EXISTS api_keys DROP COLUMN IF EXISTS rolled_at;
