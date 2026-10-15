-- Deleting a user sets posts.created_by_user to NULL: without an index
-- each delete scans every post. Built concurrently, so writes go on
-- meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS posts_created_by_user_idx ON posts (created_by_user) WHERE created_by_user IS NOT NULL;
