-- Deliveries under way, by when their lease ends, for ReclaimDeliveries.
-- Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS webhook_deliveries_lease_idx ON webhook_deliveries (lease_until) WHERE status = 'delivering';
