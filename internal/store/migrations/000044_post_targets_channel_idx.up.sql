-- Deleting a channel cascades to its targets through (org_id, channel_id):
-- without an index each delete scans every target. Built concurrently, so
-- writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS post_targets_channel_idx ON post_targets (org_id, channel_id);
