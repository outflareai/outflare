-- migration-safety: the version before this one (migrations 0431 to 0433) never upserts against the per-workspace keys dropped here: campaigns and topics insert with a bare ON CONFLICT DO NOTHING, leads upsert on (brand_id, lower(email)), a lead list is inserted with a bare ON CONFLICT DO NOTHING, and a template insert that breaks a unique rule is refused the same way by the per-brand one.
-- Brands own their data (plans/brands/README.md), the contract for names:
-- two brands of a workspace can use the same campaign slug, email topic
-- key, template handle and list name, and the same address can be a lead
-- in both. The per-brand unique indexes are now the only ones.

-- A topic the version before 0431 made has no brand: it goes to the first
-- brand, like every topic before it.
UPDATE email_topics SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

ALTER TABLE tracking_campaigns DROP CONSTRAINT tracking_campaigns_workspace_id_slug_key;
ALTER TABLE email_topics DROP CONSTRAINT email_topics_workspace_id_key_key;
ALTER TABLE email_templates DROP CONSTRAINT email_templates_workspace_id_handle_key;
DROP INDEX leads_email_idx;
DROP INDEX lead_lists_name_idx;

-- Lookups by workspace the dropped keys served.
CREATE INDEX tracking_campaigns_workspace_slug_idx ON tracking_campaigns (workspace_id, slug);
CREATE INDEX email_topics_workspace_key_idx ON email_topics (workspace_id, key);
CREATE INDEX email_templates_workspace_handle_idx ON email_templates (workspace_id, handle);
CREATE INDEX leads_workspace_email_idx ON leads (workspace_id, lower(email));
CREATE INDEX lead_lists_workspace_name_idx ON lead_lists (workspace_id, lower(name));
