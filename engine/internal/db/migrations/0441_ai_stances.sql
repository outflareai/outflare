-- AI visibility stances: once a run has its answers, the workspace's own
-- model reads each one and says, for the brand and each competitor it
-- names, whether the answer recommends it, only lists it or warns against
-- it; which other brands it recommends or lists (suggested competitors);
-- and what kind of site each cited page is.

ALTER TABLE ai_samples
    -- '': not read yet; judged; skipped (no model, or nothing named);
    -- failed.
    ADD COLUMN stance_status text NOT NULL DEFAULT '' CHECK (stance_status IN ('', 'judged', 'skipped', 'failed')),
    -- Brands the answer recommends or lists that the cohort doesn't track.
    ADD COLUMN other_brands  text[] NOT NULL DEFAULT '{}',
    -- What reading it cost on the workspace's own model.
    ADD COLUMN stance_cost_cents integer NOT NULL DEFAULT 0;

ALTER TABLE ai_mentions
    -- recommended: a top or clear pick; listed: one option among others;
    -- warned: the answer advises against it or says something bad.
    ADD COLUMN stance       text NOT NULL DEFAULT '' CHECK (stance IN ('', 'recommended', 'listed', 'warned')),
    -- The answer's own words for it, at most 300 characters.
    ADD COLUMN stance_quote text NOT NULL DEFAULT '';

ALTER TABLE ai_citations
    -- What kind of site the page is on.
    ADD COLUMN source_type text NOT NULL DEFAULT '' CHECK (source_type IN ('', 'brand', 'competitor', 'review', 'comparison', 'forum', 'news', 'video', 'social', 'directory', 'docs', 'blog', 'other'));

-- Runs look for answers not read yet.
CREATE INDEX ai_samples_stance_todo_idx ON ai_samples (run_id) WHERE status = 'ok' AND stance_status = '';
