SET LOCAL lock_timeout = '5s';

DROP TABLE operator_keys;
ALTER TABLE orgs DROP COLUMN limit_posts_month;
ALTER TABLE orgs DROP COLUMN limit_members;
ALTER TABLE orgs DROP COLUMN limit_channels;
ALTER TABLE orgs DROP COLUMN limit_brands;
ALTER TABLE orgs DROP COLUMN external_ref;
ALTER TABLE orgs DROP COLUMN status_note;
ALTER TABLE orgs DROP COLUMN status;
