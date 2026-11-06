-- AI visibility's question library: each question says how much the
-- person asking already knows (awareness) and what they ask for (the
-- question type), for whom and on what topic, and how many people ask it.
-- Questions the workspace's AI drafts wait as suggested until a person
-- keeps them; runs ask only active ones.

ALTER TABLE ai_prompts
    -- problem: knows the problem, not the kind of solution; solution:
    -- knows the kind of solution, not the brands; competitor: knows a
    -- competitor; brand: knows the brand.
    ADD COLUMN awareness     text NOT NULL DEFAULT '' CHECK (awareness IN ('', 'problem', 'solution', 'competitor', 'brand')),
    -- how_to (how do I do this task), fix (how do I fix this issue), best
    -- (who's best at this), alternatives (alternatives to a competitor),
    -- versus (X vs Y), supplier (best supplier for a need), about (about
    -- the brand: is it legit, what does it cost).
    ADD COLUMN question_type text NOT NULL DEFAULT '' CHECK (question_type IN ('', 'how_to', 'fix', 'best', 'alternatives', 'versus', 'supplier', 'about')),
    -- Who asks it (a role or kind of buyer) and what it is about, both
    -- free text for grouping.
    ADD COLUMN persona       text NOT NULL DEFAULT '',
    ADD COLUMN topic         text NOT NULL DEFAULT '',
    -- The competitor an alternatives or versus question names.
    ADD COLUMN competitor    text NOT NULL DEFAULT '',
    -- About how many people ask it a month, and where that came from
    -- (manual: a person said; search_console: the brand's own search
    -- queries; ai_search: how often people ask AI assistants, from the
    -- workspace's own DataForSEO), for weighting the score.
    ADD COLUMN demand        integer CHECK (demand >= 0),
    ADD COLUMN demand_source text NOT NULL DEFAULT '' CHECK (demand_source IN ('', 'manual', 'search_console', 'ai_search')),
    -- manual: a person added it; drafted: the workspace's AI suggested it,
    -- with why.
    ADD COLUMN source        text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'drafted')),
    ADD COLUMN why           text NOT NULL DEFAULT '';

-- Only widens the check (suggested), so the version before still works:
-- its runs ask active prompts only.
ALTER TABLE ai_prompts DROP CONSTRAINT ai_prompts_status_check;
ALTER TABLE ai_prompts ADD CONSTRAINT ai_prompts_status_check CHECK (status IN ('active', 'paused', 'suggested'));
