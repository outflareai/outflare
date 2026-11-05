-- Presence: where competitors appear. Each tracked competitor (an SEO
-- competitor with the brand) is scanned on its own schedule: the brand's
-- top competitors weekly and the rest monthly by default, both settable
-- per brand, and any one competitor can have its own cadence. A scan
-- reads search results for the competitor's name, who links to the
-- competitor's site and pages AI answers cite when they name it, each on
-- the workspace's own connections. Every page found is kept with its type,
-- whether it looks paid for (with the evidence) and the site's numbers.

ALTER TABLE brands
    ADD COLUMN presence_top_competitors integer NOT NULL DEFAULT 10 CHECK (presence_top_competitors BETWEEN 0 AND 1000),
    ADD COLUMN presence_top_every_days integer NOT NULL DEFAULT 7 CHECK (presence_top_every_days BETWEEN 1 AND 90),
    ADD COLUMN presence_rest_every_days integer NOT NULL DEFAULT 30 CHECK (presence_rest_every_days BETWEEN 1 AND 90);

ALTER TABLE seo_competitors
    -- Its own cadence in days; null follows the brand's.
    ADD COLUMN presence_every_days integer CHECK (presence_every_days BETWEEN 1 AND 90),
    -- When its last scan was queued and when one finished, with what
    -- each source said ('' when it worked): {"web_search": "connector_needed"}.
    ADD COLUMN presence_queued_at timestamptz,
    ADD COLUMN presence_scanned_at timestamptz,
    ADD COLUMN presence_outcome jsonb NOT NULL DEFAULT '{}';
CREATE INDEX seo_competitors_presence_idx ON seo_competitors (presence_queued_at NULLS FIRST) WHERE status = 'tracked';

-- A read of who links to a competitor's site, for its presence.
ALTER TABLE link_reads DROP CONSTRAINT link_reads_reason_check;
ALTER TABLE link_reads ADD CONSTRAINT link_reads_reason_check CHECK (reason IN ('requested', 'dead_page', 'competitor_presence'));

CREATE TABLE coverage_competitor_placements (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    competitor_id  uuid NOT NULL REFERENCES seo_competitors (id) ON DELETE CASCADE,
    url            text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    -- The page's site, lowercase, without www.
    host           text NOT NULL CHECK (length(host) BETWEEN 1 AND 253),
    title          text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    -- A placement type from the address, or '' when it says nothing.
    kind           text NOT NULL DEFAULT '' CHECK (length(kind) <= 40),
    -- likely_paid only with evidence; nobody outside the deal can be sure.
    arrangement    text NOT NULL DEFAULT 'unknown' CHECK (arrangement IN ('likely_paid', 'unknown')),
    evidence       text[] NOT NULL DEFAULT '{}',
    -- Which scans found it: web_search, backlinks, ai_citations.
    sources        text[] NOT NULL DEFAULT '{}',
    -- From a backlinks read: the link to the competitor and its anchor.
    links_to_them  boolean NOT NULL DEFAULT false,
    nofollow       boolean,
    anchor         text NOT NULL DEFAULT '' CHECK (length(anchor) <= 500),
    -- The site's numbers by the workspace's SEO connection, when read.
    domain_rating  double precision,
    url_rating     double precision,
    domain_traffic double precision,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    -- Set when a backlinks read reports the link lost; cleared when seen again.
    gone_at        timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (competitor_id, url)
);
CREATE INDEX coverage_competitor_placements_brand_idx ON coverage_competitor_placements (workspace_id, brand_id, last_seen_at DESC);
CREATE INDEX coverage_competitor_placements_host_idx ON coverage_competitor_placements (workspace_id, brand_id, host);
