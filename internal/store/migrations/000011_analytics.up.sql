-- Web analytics sources and their counts by day and UTM tags (ADR 0025).
-- Counts only: no visitor, session or address is ever stored.
CREATE TABLE analytics_sources (
    id           uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL,
    brand_id     uuid        NOT NULL,
    livemode     boolean     NOT NULL,
    provider     text        NOT NULL,
    site         text        NOT NULL,
    name         text        NOT NULL,
    timezone     text        NOT NULL DEFAULT '',
    goals        text[]      NOT NULL DEFAULT '{}',
    settings     jsonb       NOT NULL DEFAULT '{}',
    credentials  bytea,
    status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_reauth')),
    status_note  text        NOT NULL DEFAULT '',
    read_at      timestamptz,
    next_read_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (brand_id, livemode, provider, site),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);
CREATE INDEX analytics_sources_due_idx ON analytics_sources (next_read_at) WHERE status = 'active';

-- One row per day, tag combination and goal ('' = traffic), replaced on
-- every read: tools revise recent days.
CREATE TABLE analytics_results (
    org_id       uuid        NOT NULL,
    source_id    uuid        NOT NULL,
    day          date        NOT NULL,
    utm_source   text        NOT NULL DEFAULT '',
    utm_medium   text        NOT NULL DEFAULT '',
    utm_campaign text        NOT NULL DEFAULT '',
    utm_content  text        NOT NULL DEFAULT '',
    goal         text        NOT NULL DEFAULT '',
    visitors     bigint      NOT NULL DEFAULT 0,
    visits       bigint      NOT NULL DEFAULT 0,
    events       bigint      NOT NULL DEFAULT 0,
    read_at      timestamptz NOT NULL,
    PRIMARY KEY (source_id, day, utm_source, utm_medium, utm_campaign, utm_content, goal),
    FOREIGN KEY (org_id, source_id) REFERENCES analytics_sources (org_id, id) ON DELETE CASCADE
);
CREATE INDEX analytics_results_day_idx ON analytics_results (org_id, day);
