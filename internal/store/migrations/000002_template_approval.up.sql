-- Templates can override their brand's approval policy (ADR 0004): posts made
-- from a template follow the template's setting unless it inherits.
ALTER TABLE templates ADD COLUMN approval text NOT NULL DEFAULT 'inherit'
    CHECK (approval IN ('inherit', 'required', 'not_required'));
