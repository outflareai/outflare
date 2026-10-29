-- Flow kits add smart lists, rules and journeys. Each remembers the kit and
-- its key in the kit, so applying the kit again finds it and leaves it as
-- it is, and the builder can say which kit a draft came from.
ALTER TABLE smart_lists ADD COLUMN kit_id text;
ALTER TABLE smart_lists ADD COLUMN kit_key text;
ALTER TABLE rules ADD COLUMN kit_id text;
ALTER TABLE rules ADD COLUMN kit_key text;
ALTER TABLE journeys ADD COLUMN kit_id text;
ALTER TABLE journeys ADD COLUMN kit_key text;

CREATE UNIQUE INDEX smart_lists_kit_idx ON smart_lists (workspace_id, kit_id, kit_key) WHERE kit_key IS NOT NULL;
CREATE UNIQUE INDEX rules_kit_idx ON rules (workspace_id, kit_id, kit_key) WHERE kit_key IS NOT NULL AND status <> 'archived';
CREATE UNIQUE INDEX journeys_kit_idx ON journeys (workspace_id, kit_id, kit_key) WHERE kit_key IS NOT NULL AND status <> 'archived';
