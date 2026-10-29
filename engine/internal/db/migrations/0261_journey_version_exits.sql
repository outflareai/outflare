-- Journey versions (0249) carry the exit criteria (0246) too: a draft edits
-- them beside the steps, and publishing swaps them in with the rest.
ALTER TABLE journey_versions ADD COLUMN exit_events text[] NOT NULL DEFAULT '{}';
ALTER TABLE journey_versions ADD COLUMN exit_conditions jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(exit_conditions) = 'array');

-- Versions kept before this hold what their journey has now.
UPDATE journey_versions v SET exit_events = j.exit_events, exit_conditions = j.exit_conditions
FROM journeys j WHERE j.id = v.journey_id;
