-- Deleting a user sets media.created_by_user to NULL: without an index
-- each delete scans every media row. Built concurrently, so writes go on
-- meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS media_created_by_user_idx ON media (created_by_user) WHERE created_by_user IS NOT NULL;
