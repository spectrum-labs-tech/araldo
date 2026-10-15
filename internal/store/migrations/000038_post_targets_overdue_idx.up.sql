-- Targets that can still miss their deadline, for ExpireOverdueTargets.
-- Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS post_targets_overdue_idx ON post_targets (publish_by) WHERE status IN ('queued', 'held');
