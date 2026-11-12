-- The brand knowledge sheet (brand_context.knowledge: what the brand's
-- website says it offers) becomes something a person signs off before
-- paid ads are written from it (plans/paid-ads/plan.md, "Knowledge
-- sheet"). brand_context is one row per brand (brand_id is its key; its
-- version column counts edits), so the review lives on the row. The read's
-- failure code already lives in knowledge_error (0460), so no second error
-- column is added.
ALTER TABLE brand_context ADD COLUMN knowledge_reviewed_at timestamptz;
-- The person who said the sheet looks right.
ALTER TABLE brand_context ADD COLUMN knowledge_reviewed_by uuid REFERENCES actors (id) ON DELETE SET NULL;
-- Whether the website is read again every day: turned on when an ad
-- account is connected to the brand, so the ads are written from a sheet
-- that is at most a day old.
ALTER TABLE brand_context ADD COLUMN knowledge_refresh_daily boolean NOT NULL DEFAULT false;
-- When a read last changed what the brand sells (products, services,
-- prices or the summary). Such a read also clears the review, so the
-- owner looks again before more ads are written.
ALTER TABLE brand_context ADD COLUMN knowledge_changed_at timestamptz;

-- What the draft writer was warned about when a new ad was written
-- (a price the sheet doesn't have, a superlative, a competitor's name,
-- too few headlines, title case, pins without variants, a crowded ad
-- group): a list of {code, level, message}. Warnings only; Google's own
-- limits stay the only refusals.
ALTER TABLE ad_drafts ADD COLUMN checks jsonb NOT NULL DEFAULT '[]';
