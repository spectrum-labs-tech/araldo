-- Brands by email logo: deleting media checks the foreign key, and
-- UnusedMedia looks for media no brand uses. Built concurrently, so writes
-- go on meanwhile (ADR 0029).
CREATE INDEX CONCURRENTLY IF NOT EXISTS brands_email_logo_idx ON brands (org_id, email_logo_media_id) WHERE email_logo_media_id IS NOT NULL;
