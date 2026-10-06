SET LOCAL lock_timeout = '5s';

-- The monthly report by email (ADR 0026 phase 3): who gets a brand's report,
-- each month's mailing, and each recipient's email.
CREATE TABLE report_recipients (
    org_id     uuid        NOT NULL,
    brand_id   uuid        NOT NULL,
    email      text        NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, brand_id, email),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);

-- One per brand and month, made once: the share link the emails carry,
-- its token sealed with the org's data key so retries can send it.
CREATE TABLE report_mailings (
    org_id      uuid        NOT NULL,
    brand_id    uuid        NOT NULL,
    month       text        NOT NULL,
    share_id    uuid        NOT NULL,
    share_token bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, brand_id, month),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);

CREATE TABLE report_emails (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL,
    brand_id        uuid        NOT NULL,
    month           text        NOT NULL,
    email           text        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('queued', 'sent', 'suppressed', 'failed')),
    reason          text        NOT NULL DEFAULT '',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, brand_id, month, email),
    FOREIGN KEY (org_id, brand_id, month) REFERENCES report_mailings (org_id, brand_id, month) ON DELETE CASCADE
);
CREATE INDEX report_emails_due_idx ON report_emails (next_attempt_at) WHERE status = 'queued';
