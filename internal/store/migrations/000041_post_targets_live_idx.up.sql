-- An org's targets still in the queue's hands, for QueueStats, without
-- reading its history. Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS post_targets_live_idx ON post_targets (org_id, livemode) WHERE status IN ('queued', 'publishing', 'needs_attention');
