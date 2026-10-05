-- A key on (org_id, id, livemode), so rows of one mode can reference only
-- rows of the same mode (ADR 0006); see 000018. Built concurrently, so
-- writes go on meanwhile (ADR 0029).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS channels_mode_key ON channels (org_id, id, livemode);
