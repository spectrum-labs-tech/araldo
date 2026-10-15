-- An org's failed targets and those that need a person, by when they last
-- changed, for reports. Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS post_targets_trouble_idx ON post_targets (org_id, livemode, updated_at) WHERE status IN ('failed', 'needs_attention');
