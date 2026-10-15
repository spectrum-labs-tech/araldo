-- Idempotency keys by age, for PruneIdempotencyKeys. Built concurrently,
-- so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idempotency_keys_created_idx ON idempotency_keys (created_at);
