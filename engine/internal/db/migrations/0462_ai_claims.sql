-- What AI answers say about each brand: the attributes they give it ("cheap",
-- "190 countries", "slow support") and the objections they raise against it
-- or in favour of picking another. Read from each answer's words by the
-- workspace's own model, together with the stances, so a brand can see
-- what it is known for, what is held against it, and dispute a claim
-- that isn't true (a fix, ai_wrong_claim).
CREATE TABLE ai_claims (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    prompt_id    uuid NOT NULL REFERENCES ai_prompts (id) ON DELETE CASCADE,
    sample_id    uuid NOT NULL REFERENCES ai_samples (id) ON DELETE CASCADE,
    -- Who the claim is about: the brand, or a competitor by its stored name.
    entity       text NOT NULL CHECK (entity IN ('brand', 'competitor')),
    name         text NOT NULL,
    -- attribute: what the answer says it is or has; objection: a reason the
    -- answer gives against it, or for picking another instead.
    kind         text NOT NULL CHECK (kind IN ('attribute', 'objection')),
    -- The claim as a short phrase, and its key for grouping (lower case,
    -- single spaces, no trailing punctuation).
    text         text NOT NULL,
    key          text NOT NULL,
    sentiment    text NOT NULL CHECK (sentiment IN ('positive', 'negative', 'neutral')),
    -- The answer's own words (at most 300 characters).
    quote        text NOT NULL DEFAULT '',
    sampled_at   timestamptz NOT NULL
);
CREATE INDEX ai_claims_brand_idx ON ai_claims (workspace_id, brand_id, sampled_at DESC);
CREATE INDEX ai_claims_sample_idx ON ai_claims (sample_id);
