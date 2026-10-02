-- A post can be reviewed by an API key with posts:approve (ADR 0019).
ALTER TABLE posts ADD COLUMN reviewed_by_key uuid;
