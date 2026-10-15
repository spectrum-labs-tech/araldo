-- Sessions by expiry, for PruneSessions. Built concurrently, so writes go
-- on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS sessions_expires_idx ON sessions (expires_at);
