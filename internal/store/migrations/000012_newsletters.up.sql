-- Newsletters (ADR 0024): Araldo designs, approves and schedules issues;
-- each mail account's provider owns its list and sends. No subscriber is
-- ever stored: only audiences' IDs, names and sizes, and the counts the
-- providers report.

-- Each brand's look in email. The postal address is required to schedule.
ALTER TABLE brands
    ADD COLUMN email_logo_media_id uuid,
    ADD COLUMN email_accent        text NOT NULL DEFAULT '',
    ADD COLUMN email_address       text NOT NULL DEFAULT '',
    ADD COLUMN email_footer        text NOT NULL DEFAULT '',
    ADD CONSTRAINT brands_email_logo_fk FOREIGN KEY (org_id, email_logo_media_id) REFERENCES media (org_id, id);

CREATE TABLE mail_accounts (
    id                uuid        NOT NULL PRIMARY KEY,
    org_id            uuid        NOT NULL,
    brand_id          uuid        NOT NULL,
    livemode          boolean     NOT NULL,
    provider          text        NOT NULL,
    external_id       text        NOT NULL,
    name              text        NOT NULL,
    from_name         text        NOT NULL,
    from_email        text        NOT NULL,
    reply_to          text        NOT NULL DEFAULT '',
    default_audiences text[]      NOT NULL DEFAULT '{}',
    settings          jsonb       NOT NULL DEFAULT '{}',
    credentials       bytea,
    status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_reauth')),
    status_note       text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (brand_id, livemode, provider, external_id, from_email),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);

CREATE TABLE newsletter_issues (
    id               uuid        NOT NULL PRIMARY KEY,
    org_id           uuid        NOT NULL,
    brand_id         uuid        NOT NULL,
    livemode         boolean     NOT NULL,
    subject          text        NOT NULL,
    preview_text     text        NOT NULL DEFAULT '',
    body             text        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('draft', 'pending_approval', 'scheduled', 'sending', 'sent',
                                                            'partially_sent', 'canceled', 'failed')),
    send_at          timestamptz,
    approval_needed  boolean     NOT NULL DEFAULT false,
    reviewed_by_user uuid        REFERENCES users (id) ON DELETE SET NULL,
    reviewed_by_key  uuid,
    reviewed_at      timestamptz,
    review_note      text        NOT NULL DEFAULT '',
    created_by_user  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_key   uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);
CREATE INDEX newsletter_issues_list_idx ON newsletter_issues (org_id, livemode, id);

-- One delivery per mail account an issue goes to, as a post has one target
-- per channel. The account's name and provider are kept for history.
CREATE TABLE newsletter_deliveries (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL,
    issue_id        uuid        NOT NULL,
    mail_account_id uuid,
    livemode        boolean     NOT NULL,
    provider        text        NOT NULL,
    account_name    text        NOT NULL,
    audiences       jsonb       NOT NULL DEFAULT '[]',
    status          text        NOT NULL CHECK (status IN ('draft', 'held', 'queued', 'handed_off', 'sent', 'canceled', 'failed',
                                                           'needs_attention')),
    handoff_at      timestamptz,
    lease_until     timestamptz,
    attempts        integer     NOT NULL DEFAULT 0,
    campaign_id     text        NOT NULL DEFAULT '',
    last_error      text        NOT NULL DEFAULT '',
    sent_at         timestamptz,
    recipients      bigint      NOT NULL DEFAULT 0,
    delivered       bigint      NOT NULL DEFAULT 0,
    opens           bigint      NOT NULL DEFAULT 0,
    clicks          bigint      NOT NULL DEFAULT 0,
    unsubscribes    bigint      NOT NULL DEFAULT 0,
    bounces         bigint      NOT NULL DEFAULT 0,
    complaints      bigint      NOT NULL DEFAULT 0,
    results_read_at timestamptz,
    next_read_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (issue_id, mail_account_id),
    FOREIGN KEY (org_id, issue_id) REFERENCES newsletter_issues (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, mail_account_id) REFERENCES mail_accounts (org_id, id) ON DELETE SET NULL (mail_account_id)
);
CREATE INDEX newsletter_deliveries_handoff_idx ON newsletter_deliveries (handoff_at) WHERE status = 'queued';
CREATE INDEX newsletter_deliveries_read_idx ON newsletter_deliveries (next_read_at) WHERE status IN ('handed_off', 'sent');

-- The library images an issue uses, so they are not pruned.
CREATE TABLE newsletter_media (
    org_id   uuid NOT NULL,
    issue_id uuid NOT NULL,
    media_id uuid NOT NULL,
    PRIMARY KEY (issue_id, media_id),
    FOREIGN KEY (org_id, issue_id) REFERENCES newsletter_issues (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, media_id) REFERENCES media (org_id, id)
);
CREATE INDEX newsletter_media_media_idx ON newsletter_media (media_id);
