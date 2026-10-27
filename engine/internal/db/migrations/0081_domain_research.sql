-- A workspace's own research into email domains the global data cannot
-- place. The classifier answers most domains from Outflare's reviewed
-- providers, the public lists and DNS; what is left is the long tail,
-- where the domain's own homepage usually says what it is.
--
-- Research runs on the workspace's own keys: its scraping connector reads
-- the homepage and its own AI model reads the words. So a row here belongs
-- to one workspace, is never shared, and never changes the global data.
CREATE TABLE domain_research (
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The canonical domain, as the classifier normalises it.
    domain            text NOT NULL CHECK (length(domain) BETWEEN 1 AND 253),
    category          text NOT NULL CHECK (length(category) <= 60),
    subcategory       text CHECK (length(subcategory) <= 60),
    confidence_status text NOT NULL CHECK (length(confidence_status) <= 40),
    -- Signals from the classifier's fixed list, and the attributes the
    -- page settled, as name -> true/false/unknown.
    signals           jsonb NOT NULL DEFAULT '[]',
    attributes        jsonb NOT NULL DEFAULT '{}',
    -- The model's own one-line reason, for a person reading the record.
    note              text NOT NULL DEFAULT '' CHECK (length(note) <= 400),
    homepage_url      text NOT NULL DEFAULT '' CHECK (length(homepage_url) <= 2048),
    homepage_status   int,
    model             text NOT NULL DEFAULT '' CHECK (length(model) <= 200),
    -- What this one domain cost: US cents of AI on the workspace's own
    -- provider bill, and the units its scraper charged.
    cost_cents        bigint NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    page_units        numeric(12, 4) NOT NULL DEFAULT 0 CHECK (page_units >= 0),
    researched_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, domain)
);

-- Summing a month's research spend, and listing what was researched last.
CREATE INDEX domain_research_recent ON domain_research (workspace_id, researched_at DESC);

-- Research is off until a workspace turns it on, because it spends the
-- workspace's own money with two providers.
ALTER TABLE workspace_ai_settings ADD COLUMN domain_research boolean NOT NULL DEFAULT false;

-- The most domain research may cost in a calendar month (UTC), separate
-- from the workspace's overall AI limit so research cannot eat a mission's
-- budget. Research stops at whichever limit comes first. Null: no limit of
-- its own. US$200 by default.
ALTER TABLE workspace_ai_settings ADD COLUMN domain_research_limit_cents bigint DEFAULT 20000
    CHECK (domain_research_limit_cents >= 0);
