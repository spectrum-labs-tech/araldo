-- Araldo's first schema. Conventions (ADR 0004, 0006):
--   * every tenant-owned table has org_id, and children reference parents
--     by (org_id, id), so a row can never point into another org;
--   * mode-scoped (activity) tables have livemode;
--   * ids are UUIDv7 made by the application.

-- Encryption (ADR 0008): data keys wrapped by a master key. scope is an
-- org id, or the nil UUID for the install.
CREATE TABLE data_keys (
    scope      uuid        NOT NULL,
    version    integer     NOT NULL CHECK (version > 0),
    kek_id     text        NOT NULL,
    wrapped    bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, version)
);

-- People (ADR 0007). Users are global; memberships join them to orgs.
CREATE TABLE users (
    id               uuid        PRIMARY KEY,
    email            text        NOT NULL,
    email_normalized text        NOT NULL UNIQUE,
    name             text        NOT NULL DEFAULT '',
    password_hash    text,
    totp_secret      bytea,
    totp_last_step   bigint      NOT NULL DEFAULT 0,
    totp_enabled_at  timestamptz,
    failed_logins    integer     NOT NULL DEFAULT 0,
    locked_until     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recovery_codes (
    id         uuid        PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  bytea       NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX recovery_codes_user_idx ON recovery_codes (user_id);

CREATE TABLE sessions (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL UNIQUE,
    csrf_token   text        NOT NULL,
    -- pending_mfa: password accepted, second factor still owed.
    state        text        NOT NULL CHECK (state IN ('pending_mfa', 'active')),
    current_org  uuid,
    livemode     boolean     NOT NULL DEFAULT false,
    sudo_until   timestamptz,
    user_agent   text        NOT NULL DEFAULT '',
    ip           text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Tenancy (ADR 0004).
CREATE TABLE orgs (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL,
    require_mfa boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

CREATE TABLE brands (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name            text        NOT NULL,
    slug            text        NOT NULL,
    timezone        text        NOT NULL DEFAULT 'UTC',
    approval_policy text        NOT NULL DEFAULT 'none'
        CHECK (approval_policy IN ('none', 'required_for_editors_and_keys', 'required_for_all')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, slug)
);

-- Weekly publishing slots in the brand's time zone (ADR 0011).
CREATE TABLE schedule_slots (
    id            uuid     NOT NULL PRIMARY KEY,
    org_id        uuid     NOT NULL,
    brand_id      uuid     NOT NULL,
    weekday       smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    minute_of_day integer  NOT NULL CHECK (minute_of_day BETWEEN 0 AND 1439),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE,
    UNIQUE (brand_id, weekday, minute_of_day)
);

-- API keys (ADR 0006): only a hash is kept.
CREATE TABLE api_keys (
    id           uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    livemode     boolean     NOT NULL,
    name         text        NOT NULL,
    hint         text        NOT NULL,
    key_hash     bytea       NOT NULL UNIQUE,
    scopes       text[]      NOT NULL DEFAULT '{}',
    brand_id     uuid,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);
CREATE INDEX api_keys_org_idx ON api_keys (org_id, livemode);

-- Connected accounts (ADR 0009).
CREATE TABLE channels (
    id           uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL,
    brand_id     uuid        NOT NULL,
    livemode     boolean     NOT NULL,
    provider     text        NOT NULL,
    -- For sandbox channels: the platform whose rules apply.
    emulates     text,
    display_name text        NOT NULL DEFAULT '',
    handle       text        NOT NULL DEFAULT '',
    external_id  text        NOT NULL DEFAULT '',
    profile_url  text        NOT NULL DEFAULT '',
    settings     jsonb       NOT NULL DEFAULT '{}',
    credentials  bytea,
    status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_reauth', 'disabled')),
    status_note  text        NOT NULL DEFAULT '',
    hold_until   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE,
    CHECK ((provider = 'sandbox') = (NOT livemode)),
    CHECK ((provider = 'sandbox') = (emulates IS NOT NULL))
);
CREATE INDEX channels_brand_idx ON channels (org_id, brand_id, livemode);

-- Templates (ADR 0010): versions are immutable.
CREATE TABLE templates (
    id             uuid        NOT NULL PRIMARY KEY,
    org_id         uuid        NOT NULL,
    brand_id       uuid        NOT NULL,
    key            text        NOT NULL,
    name           text        NOT NULL DEFAULT '',
    latest_version integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (brand_id, key),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);

CREATE TABLE template_versions (
    org_id      uuid        NOT NULL,
    template_id uuid        NOT NULL,
    version     integer     NOT NULL CHECK (version > 0),
    variables   jsonb,
    examples    jsonb       NOT NULL DEFAULT '[]',
    body        text        NOT NULL,
    overrides   jsonb       NOT NULL DEFAULT '{}',
    fit         jsonb       NOT NULL DEFAULT '{}',
    created_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version),
    FOREIGN KEY (org_id, template_id) REFERENCES templates (org_id, id) ON DELETE CASCADE
);

-- Posts and their targets (ADR 0011). A target is one channel's copy of a
-- post and the unit of publishing work.
CREATE TABLE posts (
    id               uuid        NOT NULL PRIMARY KEY,
    org_id           uuid        NOT NULL,
    brand_id         uuid        NOT NULL,
    livemode         boolean     NOT NULL,
    status           text        NOT NULL CHECK (status IN (
        'pending_approval', 'scheduled', 'publishing', 'published', 'partially_published', 'failed', 'canceled', 'rejected')),
    template_id      uuid,
    template_version integer,
    data             jsonb,
    content          jsonb,
    publish_at       timestamptz NOT NULL,
    publish_by       timestamptz NOT NULL,
    slot_at          timestamptz,
    metadata         jsonb       NOT NULL DEFAULT '{}',
    approval_needed  boolean     NOT NULL DEFAULT false,
    reviewed_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    reviewed_at      timestamptz,
    review_note      text        NOT NULL DEFAULT '',
    created_by_user  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_key   uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, template_id) REFERENCES templates (org_id, id) ON DELETE SET NULL (template_id)
);
CREATE INDEX posts_list_idx ON posts (org_id, livemode, id DESC);
-- One post per brand slot (next_slot scheduling).
CREATE UNIQUE INDEX posts_slot_idx ON posts (brand_id, livemode, slot_at)
    WHERE slot_at IS NOT NULL AND status NOT IN ('canceled', 'rejected');

CREATE TABLE post_targets (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL,
    post_id         uuid        NOT NULL,
    channel_id      uuid        NOT NULL,
    livemode        boolean     NOT NULL,
    provider        text        NOT NULL,
    parts           jsonb       NOT NULL,
    status          text        NOT NULL CHECK (status IN (
        'held', 'queued', 'publishing', 'published', 'failed', 'needs_attention', 'canceled')),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    publish_by      timestamptz NOT NULL,
    lease_owner     text,
    lease_until     timestamptz,
    posted          jsonb       NOT NULL DEFAULT '[]',
    permalink       text        NOT NULL DEFAULT '',
    error_code      text        NOT NULL DEFAULT '',
    error_message   text        NOT NULL DEFAULT '',
    published_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (post_id, channel_id),
    FOREIGN KEY (org_id, post_id) REFERENCES posts (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, channel_id) REFERENCES channels (org_id, id) ON DELETE CASCADE
);
CREATE INDEX post_targets_due_idx ON post_targets (next_attempt_at) WHERE status = 'queued';
CREATE INDEX post_targets_publishing_idx ON post_targets (lease_until) WHERE status = 'publishing';
CREATE INDEX post_targets_post_idx ON post_targets (post_id);

CREATE TABLE publish_attempts (
    id           uuid        NOT NULL PRIMARY KEY,
    org_id       uuid        NOT NULL,
    target_id    uuid        NOT NULL,
    attempt      integer     NOT NULL,
    started_at   timestamptz NOT NULL,
    finished_at  timestamptz,
    outcome      text        NOT NULL DEFAULT 'running',
    error_code   text        NOT NULL DEFAULT '',
    error        text        NOT NULL DEFAULT '',
    FOREIGN KEY (org_id, target_id) REFERENCES post_targets (org_id, id) ON DELETE CASCADE
);
CREATE INDEX publish_attempts_target_idx ON publish_attempts (target_id);

-- Events and webhooks (ADR 0012).
CREATE TABLE events (
    id         uuid        NOT NULL PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    livemode   boolean     NOT NULL,
    type       text        NOT NULL,
    data       jsonb       NOT NULL,
    request_id text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id)
);
CREATE INDEX events_list_idx ON events (org_id, livemode, id DESC);

CREATE TABLE webhook_endpoints (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    livemode        boolean     NOT NULL,
    url             text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    event_types     text[]      NOT NULL,
    secret          bytea       NOT NULL,
    status          text        NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    disabled_reason text        NOT NULL DEFAULT '',
    failing_since   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id)
);

CREATE TABLE webhook_deliveries (
    id               uuid        NOT NULL PRIMARY KEY,
    org_id           uuid        NOT NULL,
    endpoint_id      uuid        NOT NULL,
    event_id         uuid        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('pending', 'delivering', 'succeeded', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    lease_owner      text,
    lease_until      timestamptz,
    response_status  integer,
    response_body    text        NOT NULL DEFAULT '',
    error            text        NOT NULL DEFAULT '',
    duration_ms      integer,
    last_attempt_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, endpoint_id) REFERENCES webhook_endpoints (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, event_id) REFERENCES events (org_id, id) ON DELETE CASCADE
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (endpoint_id, id DESC);

-- API idempotency (ADR 0005).
CREATE TABLE idempotency_keys (
    api_key_id      uuid        NOT NULL,
    key             text        NOT NULL,
    fingerprint     bytea       NOT NULL,
    status          text        NOT NULL CHECK (status IN ('in_progress', 'done')),
    response_status integer,
    response_body   bytea,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, key)
);

-- Audit log (ADR 0004): append-only.
CREATE TABLE audit_events (
    id         uuid        NOT NULL PRIMARY KEY,
    org_id     uuid,
    actor_user uuid,
    actor_key  uuid,
    action     text        NOT NULL,
    target     text        NOT NULL DEFAULT '',
    outcome    text        NOT NULL DEFAULT 'ok',
    request_id text        NOT NULL DEFAULT '',
    detail     jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_org_idx ON audit_events (org_id, id DESC);

-- Background task leases (caseline ADR 0019).
CREATE TABLE scheduled_tasks (
    name        text        PRIMARY KEY,
    enabled     boolean     NOT NULL DEFAULT true,
    interval_ms bigint      NOT NULL,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    failures    integer     NOT NULL DEFAULT 0,
    last_error  text        NOT NULL DEFAULT '',
    last_run_at timestamptz,
    last_ok_at  timestamptz
);
