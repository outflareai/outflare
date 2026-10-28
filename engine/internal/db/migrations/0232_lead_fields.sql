-- A lead's details live on its person (engine/internal/customers,
-- SyncLeads): name, job title, LinkedIn, phone and company are copied
-- onto the person record whenever a lead is added, imported, edited or
-- enriched, and cold email's merge tags read the person back. This copies
-- what existing leads know, a workspace at a time in batches, so the
-- migration stays quick on big lead tables.
INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
SELECT id, 'customers.lead_fields', '{}'::jsonb, 10, 'customers.lead_fields:0232:' || id
FROM workspaces;
