SET LOCAL lock_timeout = '5s';

ALTER TABLE newsletter_issues DROP CONSTRAINT IF EXISTS newsletter_issues_template_fk;
ALTER TABLE newsletter_issues DROP COLUMN IF EXISTS data;
ALTER TABLE newsletter_issues DROP COLUMN IF EXISTS template_version;
ALTER TABLE newsletter_issues DROP COLUMN IF EXISTS template_id;
ALTER TABLE template_versions DROP COLUMN IF EXISTS preview_text;
ALTER TABLE template_versions DROP COLUMN IF EXISTS subject;
ALTER TABLE templates DROP CONSTRAINT IF EXISTS templates_kind_check;
-- Newsletter templates would read as post templates.
DELETE FROM templates WHERE kind = 'newsletter';
ALTER TABLE templates DROP COLUMN IF EXISTS kind;
