SET LOCAL lock_timeout = '5s';

-- When a key was rolled: it is replaced, and rolling it again would make a
-- second replacement. NULL until then.
ALTER TABLE api_keys ADD COLUMN rolled_at timestamptz;
