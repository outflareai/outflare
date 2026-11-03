-- A person's conversions in time order: the first-payment lookup behind
-- GET .../attribution/customers and each person's first conversion behind
-- .../attribution/cohorts read them per record, which the workspace-wide
-- index can't serve.
CREATE INDEX attribution_conversions_record_idx ON attribution_conversions (workspace_id, record_id, occurred_at) WHERE record_id IS NOT NULL;
