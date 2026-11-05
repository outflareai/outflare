-- Presence opportunities: the places worth the team's effort, ranked by
-- leverage, which is SEO and AI answers together. Outflare suggests them
-- from what it already knows: pages competitors appear on where the brand
-- isn't (competitor_gap), pages AI answers cite for the brand's questions
-- (ai_cited), placements that name the brand without linking to it
-- (unlinked_mention) and links that were removed (link_removed). Each
-- keeps the evidence and the factors behind its score, and a person or an
-- agent picks, dismisses or wins it. A suggestion is never contact: the
-- outreach still goes through its normal approval and sending checks.

CREATE TABLE placement_opportunities (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The page, as coverage keys URLs, and its site without www.
    url           text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    host          text NOT NULL CHECK (length(host) BETWEEN 1 AND 253),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    kind          text NOT NULL DEFAULT '' CHECK (length(kind) <= 40),
    reason        text NOT NULL CHECK (reason IN ('competitor_gap', 'ai_cited', 'unlinked_mention', 'link_removed')),
    -- What to do next: pitch (ask to be included), request_link, reclaim_link.
    action        text NOT NULL CHECK (action IN ('pitch', 'request_link', 'reclaim_link')),
    -- Which module suggested it: presence, ai_visibility, ...
    source        text NOT NULL DEFAULT 'presence' CHECK (length(source) BETWEEN 1 AND 40),
    -- Ids and fixed labels only: the competitors there, the placement, the
    -- AI cohort.
    evidence      jsonb NOT NULL DEFAULT '{}',
    -- The numbers behind the score: domain_rating, domain_traffic,
    -- ai_citations, competitors, and each part's points.
    factors       jsonb NOT NULL DEFAULT '{}',
    -- 0 to 100: SEO and AI leverage together.
    score         double precision NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    -- open, picked (someone is on it), dismissed, won (the brand is there
    -- now, or the link is back).
    state         text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'picked', 'dismissed', 'won')),
    owner_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    placement_id  uuid REFERENCES coverage_placements (id) ON DELETE SET NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    state_at      timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, brand_id, url, reason)
);
CREATE INDEX placement_opportunities_rank_idx ON placement_opportunities (workspace_id, brand_id, state, score DESC);
