SET LOCAL lock_timeout = '5s';

-- A person's notification email setting (ADR 0034): as things happen, or
-- one summary a day at 8:00 in their time zone.
CREATE TABLE notification_settings (
    user_id    uuid        NOT NULL PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    digest     boolean     NOT NULL DEFAULT false,
    timezone   text        NOT NULL DEFAULT 'UTC',
    updated_at timestamptz NOT NULL DEFAULT now()
);
