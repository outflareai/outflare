-- Link prospects: pages that could link to one of the workspace's SEO
-- sites, from every way of earning links (competitors' links, broken links,
-- links to win back, resource pages, mentions without a link, partners).
-- One list for every method, so a site is never pitched by two campaigns
-- at once. The site behind a page is a company record; what a prospect
-- knows about the company stays on that record.

CREATE TABLE link_prospects (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The SEO site that would get the link, and its brand, kept in step
    -- with it, for brand filters.
    site_id          uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- The company whose website the page is on.
    record_id        uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- The page that could link, as given (no fragment).
    page_url         text NOT NULL CHECK (length(page_url) <= 2048),
    -- How it was found.
    method           text NOT NULL CHECK (method IN ('competitor_gap', 'broken_link', 'reclaim', 'competitor_dead_page',
                         'resource_page', 'asset', 'unlinked_mention', 'partner', 'manual')),
    -- Our page to offer, '' until chosen.
    target_url       text NOT NULL DEFAULT '' CHECK (length(target_url) <= 2048),
    -- What was found, by method: the broken link, the competitor it links
    -- to, the mention. Ids and URLs only.
    evidence         jsonb NOT NULL DEFAULT '{}',
    -- The linking site's strength (0 to 100), its monthly search traffic
    -- and its spam score (0 clean to 100 spam), when known.
    domain_rating    double precision,
    traffic          double precision,
    spam_score       integer CHECK (spam_score BETWEEN 0 AND 100),
    -- 0 to 100, higher is worth pitching first, and why.
    score            integer NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    score_factors    jsonb NOT NULL DEFAULT '[]',
    -- prospect, then pitched, replied, agreed and live, or declined; a live
    -- link that goes away is removed. excluded is never pitched, with why.
    stage            text NOT NULL DEFAULT 'prospect' CHECK (stage IN ('prospect', 'excluded', 'pitched', 'replied',
                         'agreed', 'live', 'declined', 'removed')),
    excluded_reason  text CHECK (excluded_reason IN ('links_to_us', 'competitor', 'customer', 'partner', 'opted_out', 'spam')),
    stage_changed_at timestamptz NOT NULL DEFAULT now(),
    pitched_at       timestamptz,
    agreed_at        timestamptz,
    live_at          timestamptz,
    -- When a pitch ended without a link (declined), for the cool-down.
    ended_at         timestamptz,
    -- Who added it: manual, api, or the playbook or feature that found it.
    source           text NOT NULL DEFAULT 'manual' CHECK (length(source) <= 100),
    note             text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, page_url)
);
CREATE INDEX link_prospects_ws_idx ON link_prospects (workspace_id, stage, score DESC);
CREATE INDEX link_prospects_record_idx ON link_prospects (workspace_id, record_id);

-- A workspace's link building settings, made with the defaults on first
-- use.
CREATE TABLE link_building_settings (
    workspace_id          uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- How long after a declined pitch its website may be pitched again.
    pitch_cooldown_days   integer NOT NULL DEFAULT 60 CHECK (pitch_cooldown_days BETWEEN 0 AND 365),
    -- The weekly SEO snapshot also reads who links to pages that just went
    -- from tracked competitors' sitemaps, spending at most this share of
    -- the connection's monthly budget on it.
    weekly_reads          boolean NOT NULL DEFAULT true,
    weekly_budget_percent integer NOT NULL DEFAULT 10 CHECK (weekly_budget_percent BETWEEN 1 AND 100),
    updated_by            uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- Link reads: who links to a page or a domain that isn't the workspace's
-- own, read from its own SEO connection (paid, within the connection's
-- budget), kept so reports over them cost nothing.
CREATE TABLE link_reads (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The SEO site it was read for, if any, and its brand.
    site_id           uuid REFERENCES seo_sites (id) ON DELETE SET NULL,
    brand_id          uuid REFERENCES brands (id) ON DELETE SET NULL,
    integration_id    uuid REFERENCES integrations (id) ON DELETE SET NULL,
    connector         text NOT NULL,
    -- A page URL (scope page) or a domain (scope domain).
    target            text NOT NULL CHECK (length(target) <= 2048),
    scope             text NOT NULL CHECK (scope IN ('page', 'domain')),
    -- requested by a person, agent or playbook, or the weekly read of a
    -- competitor's dead page.
    reason            text NOT NULL CHECK (reason IN ('requested', 'dead_page')),
    competitor_id     uuid REFERENCES seo_competitors (id) ON DELETE SET NULL,
    status            text NOT NULL CHECK (status IN ('done', 'failed')),
    error_code        text,
    -- What the provider counts in all, and what was kept.
    total_links       double precision,
    links             integer NOT NULL DEFAULT 0,
    referring_domains integer NOT NULL DEFAULT 0,
    -- What it cost, in the connection's unit (USD or API unit); 0 when it
    -- came from a copy under a day old.
    cost              double precision NOT NULL DEFAULT 0,
    unit              text NOT NULL DEFAULT 'USD',
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX link_reads_target_idx ON link_reads (workspace_id, target, created_at DESC);
CREATE INDEX link_reads_cost_idx ON link_reads (integration_id, reason, created_at);

-- The links a read found.
CREATE TABLE link_read_rows (
    read_id        uuid NOT NULL REFERENCES link_reads (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    source_url     text NOT NULL,
    -- The linking site's host, lowercase, without www.
    source_domain  text NOT NULL,
    target_url     text NOT NULL,
    anchor         text NOT NULL DEFAULT '',
    nofollow       boolean NOT NULL DEFAULT false,
    lost           boolean NOT NULL DEFAULT false,
    source_rating  double precision,
    url_rating     double precision,
    domain_traffic double precision,
    spam_score     integer,
    title          text NOT NULL DEFAULT '',
    first_seen     text NOT NULL DEFAULT '',
    PRIMARY KEY (read_id, source_url, target_url)
);
CREATE INDEX link_read_rows_target_idx ON link_read_rows (workspace_id, target_url);
