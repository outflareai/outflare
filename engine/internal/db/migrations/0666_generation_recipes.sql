-- The recipe of every picture, video and voice-over a brand makes: the
-- reference files it was made from, exactly what was sent to the provider
-- and what the provider answered, and the earlier generation it reuses.
-- With these, a file a brand likes can be made again, or used as the
-- reference for the next one, so a person or a scene stays the same.

ALTER TABLE brand_generations
    -- [{"asset_id": "...", "as": "image_urls"}]: brand files sent along,
    -- each with the model's input it goes in.
    ADD COLUMN refs      jsonb NOT NULL DEFAULT '[]',
    -- What was sent, and what came back without the file itself.
    ADD COLUMN request   jsonb,
    ADD COLUMN response  jsonb,
    -- The generation whose recipe this one reuses.
    ADD COLUMN recipe_of uuid REFERENCES brand_generations (id) ON DELETE SET NULL;

-- A model that only works on a file, such as an upscaler, has no prompt.
ALTER TABLE brand_generations DROP CONSTRAINT brand_generations_prompt_check;
ALTER TABLE brand_generations ADD CONSTRAINT brand_generations_prompt_length CHECK (length(prompt) <= 100000);

-- Which generation made a file: its recipe.
CREATE INDEX brand_generations_asset_idx ON brand_generations (asset_id) WHERE asset_id IS NOT NULL;
