-- Cross-brand rules and reporting (ADR 9).

-- The cross-brand contact rule: with a number of days set, no person gets a
-- first email from one brand within that many days of an email from
-- another. Null leaves it off.
ALTER TABLE workspaces ADD COLUMN cross_brand_days int CHECK (cross_brand_days BETWEEN 1 AND 365);

-- The brand of an audit event's target when it had one, so the audit log
-- and the events feed can be filtered by brand and brand webhooks get only
-- their brand's events. No foreign key: the log outlives what it records.
ALTER TABLE audit_events ADD COLUMN brand_id uuid;
CREATE INDEX audit_events_brand_idx ON audit_events (workspace_id, brand_id, id) WHERE brand_id IS NOT NULL;

-- A webhook endpoint for one brand gets only that brand's events.
ALTER TABLE webhook_endpoints ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE CASCADE;
