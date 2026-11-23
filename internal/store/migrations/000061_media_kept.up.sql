SET LOCAL lock_timeout = '5s';

-- Media someone keeps in the library: never pruned for going unused (ADR
-- 0017). A constant default adds the column without rewriting the table.
ALTER TABLE media ADD COLUMN kept boolean NOT NULL DEFAULT false;
