-- One org per external reference (ADR 0031). Built concurrently, so writes
-- go on meanwhile (ADR 0029).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS orgs_external_ref_key ON orgs (external_ref);
