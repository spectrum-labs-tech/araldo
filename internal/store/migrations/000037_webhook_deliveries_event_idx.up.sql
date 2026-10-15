-- Deleting an event cascades to its deliveries (PruneEvents): without an
-- index on the foreign key, each deleted event scans every delivery. Built
-- concurrently, so writes go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS webhook_deliveries_event_idx ON webhook_deliveries (org_id, event_id);
