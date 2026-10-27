-- The model each AI task uses, when the workspace chose one: drafting a
-- plan, an agent turn, a routine check-in, labelling a reply, researching a
-- domain. Keyed by task, each entry is
-- {"provider", "model", "input_cents_per_million", "output_cents_per_million"}; an entry
-- for another provider than the one in use is ignored.
ALTER TABLE workspace_ai_settings ADD COLUMN task_models jsonb NOT NULL DEFAULT '{}';
