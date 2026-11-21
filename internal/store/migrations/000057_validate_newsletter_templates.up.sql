SET LOCAL lock_timeout = '5s';

-- Validates what 000056 added NOT VALID; every row since has been checked,
-- and VALIDATE lets writes go on (ADR 0029).
ALTER TABLE templates VALIDATE CONSTRAINT templates_kind_check;
ALTER TABLE newsletter_issues VALIDATE CONSTRAINT newsletter_issues_template_fk;
