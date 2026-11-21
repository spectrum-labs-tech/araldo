SET LOCAL lock_timeout = '5s';

-- Newsletter templates (ADR 0024 phase 2): a template is for posts or for
-- newsletter issues, and a newsletter template's versions render a subject
-- and preview text beside the body. An issue made from one records the
-- template, the version and the data, as a post does. The checks and the
-- key are NOT VALID, so adding them scans nothing; 000057 validates them
-- (ADR 0029).
ALTER TABLE templates ADD COLUMN kind text NOT NULL DEFAULT 'post';
ALTER TABLE templates ADD CONSTRAINT templates_kind_check CHECK (kind IN ('post', 'newsletter')) NOT VALID;
ALTER TABLE template_versions ADD COLUMN subject text NOT NULL DEFAULT '';
ALTER TABLE template_versions ADD COLUMN preview_text text NOT NULL DEFAULT '';
ALTER TABLE newsletter_issues ADD COLUMN template_id uuid;
ALTER TABLE newsletter_issues ADD COLUMN template_version integer;
ALTER TABLE newsletter_issues ADD COLUMN data jsonb;
ALTER TABLE newsletter_issues ADD CONSTRAINT newsletter_issues_template_fk
    FOREIGN KEY (org_id, template_id) REFERENCES templates (org_id, id) ON DELETE SET NULL (template_id) NOT VALID;
