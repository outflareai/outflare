-- AI read checks of a brand's own pages: the workspace's own AI model
-- reads a page as an AI answer would and says whether it answers up front,
-- which questions it answers, which facts are unclear and what to change.
-- One row per brand and page; checking again replaces the result.
--
-- Expand only: a new table the previous version doesn't read.
CREATE TABLE ai_page_reviews (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    url          text NOT NULL,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'reading', 'ready', 'failed')),
    error        text NOT NULL DEFAULT '',
    -- The model's answer: score, answers_up_front, summary, questions,
    -- unclear and changes.
    result       jsonb,
    model        text NOT NULL DEFAULT '',
    requested_by uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    reviewed_at  timestamptz,
    UNIQUE (brand_id, url)
);
CREATE INDEX ai_page_reviews_brand_idx ON ai_page_reviews (workspace_id, brand_id, updated_at DESC);
