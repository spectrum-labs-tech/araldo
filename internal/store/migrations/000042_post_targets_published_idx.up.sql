-- An org's published targets by when they went out, for usage and reports.
-- Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS post_targets_published_idx ON post_targets (org_id, livemode, published_at) WHERE published_at IS NOT NULL;
