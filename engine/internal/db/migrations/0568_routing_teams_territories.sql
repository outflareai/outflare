-- Routing by team and by territory.

-- A rule's pool can be a team in place of a list of people: whoever is in
-- the team when a record is routed shares it, so adding a teammate to
-- the team is enough. When the team is deleted the rule has no pool
-- until someone picks one.
ALTER TABLE routing_rules ADD COLUMN team_id uuid REFERENCES teams (id) ON DELETE SET NULL;
CREATE INDEX routing_rules_team ON routing_rules (team_id) WHERE team_id IS NOT NULL;

-- A rule's territory: the countries (two-letter codes) it takes. None
-- means anywhere. A record with no country is in no territory.
ALTER TABLE routing_rules ADD COLUMN countries text[] NOT NULL DEFAULT '{}';
