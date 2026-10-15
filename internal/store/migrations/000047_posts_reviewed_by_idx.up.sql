-- Deleting a user sets posts.reviewed_by to NULL: without an index each
-- delete scans every post. Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS posts_reviewed_by_idx ON posts (reviewed_by) WHERE reviewed_by IS NOT NULL;
