-- Engagement (ADR 0018): the latest reading of each published target, when
-- to read it next, and every reading.
CREATE TABLE target_engagement (
    target_id    uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL,
    state        text        NOT NULL DEFAULT 'collecting'
        CHECK (state IN ('collecting', 'done', 'deleted', 'unsupported')),
    likes        bigint      NOT NULL DEFAULT 0,
    reposts      bigint      NOT NULL DEFAULT 0,
    replies      bigint      NOT NULL DEFAULT 0,
    quotes       bigint      NOT NULL DEFAULT 0,
    views        bigint,
    read_at      timestamptz,
    next_read_at timestamptz,
    error        text        NOT NULL DEFAULT '',
    UNIQUE (org_id, target_id),
    FOREIGN KEY (org_id, target_id) REFERENCES post_targets (org_id, id) ON DELETE CASCADE
);
CREATE INDEX target_engagement_due_idx ON target_engagement (next_read_at) WHERE next_read_at IS NOT NULL;

CREATE TABLE engagement_readings (
    org_id    uuid        NOT NULL,
    target_id uuid        NOT NULL,
    read_at   timestamptz NOT NULL,
    likes     bigint      NOT NULL,
    reposts   bigint      NOT NULL,
    replies   bigint      NOT NULL,
    quotes    bigint      NOT NULL,
    views     bigint,
    PRIMARY KEY (target_id, read_at),
    FOREIGN KEY (org_id, target_id) REFERENCES target_engagement (org_id, target_id) ON DELETE CASCADE
);

-- Targets published before engagement existed are read soon, then on the
-- schedule.
INSERT INTO target_engagement (target_id, org_id, next_read_at)
SELECT id, org_id, now() FROM post_targets WHERE status = 'published';
