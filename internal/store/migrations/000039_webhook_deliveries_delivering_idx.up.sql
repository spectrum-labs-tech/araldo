-- Deliveries under way, by endpoint: ClaimDeliveries counts them for each
-- endpoint it sends to. Built concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS webhook_deliveries_delivering_idx ON webhook_deliveries (endpoint_id) WHERE status = 'delivering';
