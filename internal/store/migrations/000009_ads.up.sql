-- Ad accounts and their campaigns' daily results (ADR 0023). Reading only:
-- nothing here spends.
CREATE TABLE ad_accounts (
    id           uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL,
    brand_id     uuid        NOT NULL,
    livemode     boolean     NOT NULL,
    network      text        NOT NULL,
    external_id  text        NOT NULL,
    name         text        NOT NULL,
    currency     text        NOT NULL,
    timezone     text        NOT NULL,
    settings     jsonb       NOT NULL DEFAULT '{}',
    credentials  bytea,
    -- The developer app an account connected through with a sign-in, whose
    -- credentials renew its token (ADR 0021).
    app_id       uuid,
    status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_reauth')),
    status_note  text        NOT NULL DEFAULT '',
    read_at      timestamptz,
    next_read_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (brand_id, livemode, network, external_id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, app_id) REFERENCES provider_apps (org_id, id) ON DELETE SET NULL (app_id)
);
CREATE INDEX ad_accounts_due_idx ON ad_accounts (next_read_at) WHERE status = 'active';

-- One row per campaign and day, replaced on every read: networks revise a
-- day's numbers for a while as conversions are attributed late.
CREATE TABLE ad_results (
    org_id        uuid        NOT NULL,
    ad_account_id uuid        NOT NULL,
    campaign_id   text        NOT NULL,
    campaign_name text        NOT NULL,
    day           date        NOT NULL,
    spend         bigint      NOT NULL,
    impressions   bigint      NOT NULL,
    clicks        bigint      NOT NULL,
    results       bigint      NOT NULL,
    read_at       timestamptz NOT NULL,
    PRIMARY KEY (ad_account_id, campaign_id, day),
    FOREIGN KEY (org_id, ad_account_id) REFERENCES ad_accounts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX ad_results_day_idx ON ad_results (org_id, day);
