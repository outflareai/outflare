-- Approval by template (ADR 13): a grant can also match on the change's
-- body, so a person approves a pattern once ("this template, to these
-- targets") and every change that fits it runs. Expand only: a nullable
-- column the previous version ignores.

-- A JSON object every matching change's body must contain, field for
-- field (jsonb @>). Null: the grant matches any body.
ALTER TABLE action_grants ADD COLUMN body_match jsonb CHECK (body_match IS NULL OR jsonb_typeof(body_match) = 'object');
