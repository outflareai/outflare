-- Presence step 2: discovery runs every day by itself, from more places,
-- and sets each find's type.

-- The type discovery guessed for a find (a YouTube video, a forum thread,
-- an MCP directory...), empty when it couldn't tell. Confirming the find
-- makes a placement of that type.
ALTER TABLE coverage_candidates
    ADD COLUMN kind text NOT NULL DEFAULT '' CHECK (kind = '' OR kind IN (
        'blog','comparison','news','profile','directory','review','roundup','case_study','partner','integration','resource','interview','podcast','video','community','display_ad','newsletter','sponsorship','custom',
        'influencer','youtube','forum','mcp_directory','app_listing'));

-- New places finds come from: social posts the brand's watches found, pages
-- AI answers cite when they name the brand, and the brand's web searches.
-- Widening a CHECK only expands: the previous version never writes them.
ALTER TABLE coverage_candidates DROP CONSTRAINT IF EXISTS coverage_candidates_source_check;
ALTER TABLE coverage_candidates ADD CONSTRAINT coverage_candidates_source_check CHECK (source IN (
    'manual','news','backlinks','link_prospects','import','social','ai_citations','web_search'));

-- A brand's web searches: each runs on the workspace's own search results
-- connection (DataForSEO or Geonode), within its budget, and every page it
-- returns that isn't the brand's own becomes a find.
CREATE TABLE coverage_searches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    query text NOT NULL CHECK (length(query) BETWEEN 1 AND 300),
    -- Two-letter country code; empty for the connection's default.
    country text NOT NULL DEFAULT '' CHECK (country = '' OR country ~ '^[A-Z]{2}$'),
    -- How many results to read, and how often.
    depth integer NOT NULL DEFAULT 50 CHECK (depth BETWEEN 10 AND 100),
    every_days integer NOT NULL DEFAULT 1 CHECK (every_days BETWEEN 1 AND 30),
    enabled boolean NOT NULL DEFAULT true,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    last_run_at timestamptz,
    -- What went wrong last time, as an error code (connector_needed,
    -- usage_limit_reached...); empty after a run that worked.
    last_error text NOT NULL DEFAULT '' CHECK (length(last_error) <= 100),
    last_found integer NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX coverage_searches_query_idx ON coverage_searches (workspace_id, brand_id, lower(query), country);
CREATE INDEX coverage_searches_due_idx ON coverage_searches (next_run_at) WHERE enabled;

-- When discovery last brought in the brand's finds from news, backlinks,
-- link prospects, social posts and AI answers by itself.
ALTER TABLE brands ADD COLUMN coverage_discovered_at timestamptz;
