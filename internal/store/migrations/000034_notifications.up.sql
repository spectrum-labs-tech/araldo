SET LOCAL lock_timeout = '5s';

-- Notifications (ADR 0034): one row per person, written with what caused
-- it. org_id is null for account notices. Shown is whether the dashboard
-- lists it (the person may take a type by email only).
CREATE TABLE notifications (
    id         uuid        NOT NULL PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id     uuid        REFERENCES orgs (id) ON DELETE CASCADE,
    type       text        NOT NULL,
    subject    text        NOT NULL,
    body       text        NOT NULL DEFAULT '',
    link       text        NOT NULL DEFAULT '',
    shown      boolean     NOT NULL DEFAULT true,
    dedupe_key text,
    read_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC) WHERE shown;
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE shown AND read_at IS NULL;
CREATE UNIQUE INDEX notifications_dedupe_key ON notifications (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX notifications_created_idx ON notifications (created_at);

-- A notification's email: queued until the notifications.send task sends
-- it, suppressed (with why) when it is not to be sent.
CREATE TABLE notification_emails (
    notification_id uuid        NOT NULL PRIMARY KEY REFERENCES notifications (id) ON DELETE CASCADE,
    status          text        NOT NULL CHECK (status IN ('queued', 'sent', 'suppressed', 'failed')),
    reason          text        NOT NULL DEFAULT '',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_emails_due_idx ON notification_emails (next_attempt_at) WHERE status = 'queued';

-- What a person chose for a type in an org, where it differs from the
-- type's defaults.
CREATE TABLE notification_preferences (
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    type       text        NOT NULL,
    in_app     boolean     NOT NULL,
    email      boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_id, type)
);
