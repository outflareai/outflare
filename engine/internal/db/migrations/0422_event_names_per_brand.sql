-- Brands own their data, contract step for event names
-- (plans/brands/README.md, decision 3): each brand has its own event
-- catalog, so two brands of one workspace can each define the same name.
-- event_definitions_brand_name_idx (brand_id, name), from 0322, is the
-- only unique rule left.
--
-- migration-safety: nothing upserts against (workspace_id, name) since engine#1056; the version before this one uses (brand_id, name) as its arbiter and refuses another brand's name in code, so it still works without this constraint
ALTER TABLE event_definitions DROP CONSTRAINT event_definitions_workspace_id_name_key;
CREATE INDEX event_definitions_workspace_name_idx ON event_definitions (workspace_id, name);
