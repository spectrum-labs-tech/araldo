-- Media (ADR 0017): images uploaded once and attached to posts. The file
-- lives in media_blobs (storage 'postgres') or in S3-compatible storage
-- (storage 's3', at storage_key).
CREATE TABLE media (
    id              uuid        NOT NULL PRIMARY KEY,
    org_id          uuid        NOT NULL,
    brand_id        uuid        NOT NULL,
    livemode        boolean     NOT NULL,
    content_type    text        NOT NULL,
    size_bytes      bigint      NOT NULL CHECK (size_bytes > 0),
    width           integer     NOT NULL CHECK (width > 0),
    height          integer     NOT NULL CHECK (height > 0),
    sha256          bytea       NOT NULL,
    alt             text        NOT NULL DEFAULT '',
    filename        text        NOT NULL DEFAULT '',
    storage         text        NOT NULL CHECK (storage IN ('postgres', 's3')),
    storage_key     text        NOT NULL,
    created_by_user uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_key  uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, brand_id) REFERENCES brands (org_id, id) ON DELETE CASCADE
);
CREATE INDEX media_list_idx ON media (org_id, livemode, id DESC);

CREATE TABLE media_blobs (
    media_id uuid  NOT NULL PRIMARY KEY REFERENCES media (id) ON DELETE CASCADE,
    data     bytea NOT NULL
);

-- A post's media, in order. Media a post uses cannot be deleted.
CREATE TABLE post_media (
    org_id   uuid    NOT NULL,
    post_id  uuid    NOT NULL,
    position integer NOT NULL CHECK (position >= 0),
    media_id uuid    NOT NULL,
    PRIMARY KEY (post_id, position),
    FOREIGN KEY (org_id, post_id) REFERENCES posts (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, media_id) REFERENCES media (org_id, id)
);
CREATE INDEX post_media_media_idx ON post_media (media_id);
