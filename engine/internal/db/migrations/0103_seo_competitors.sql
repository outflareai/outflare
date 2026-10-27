-- SEO competitors: the sites each of the workspace's SEO sites competes
-- with in search. SEO connections suggest them (domains ranking for the same
-- keywords); people track or dismiss them, or add their own. A tracked
-- competitor gets a light public look each week from OutflareBot (its
-- robots.txt, its sitemaps and its home page only, always obeying robots.txt),
-- and its numbers come from the workspace's own SEO connections. What
-- changed is a feed of events that are never rewritten.

CREATE TABLE seo_competitors (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id          uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- The site's brand, kept in step with it, for brand filters.
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- The competitor's domain, lowercase, without www.
    host             text NOT NULL,
    -- What people call it, and the other names it goes by (AI answers
    -- often use a short name), for features that look for it by name.
    name             text NOT NULL DEFAULT '',
    aliases          text[] NOT NULL DEFAULT '{}',
    -- suggested (a connection found it), tracked, dismissed (not one).
    status           text NOT NULL DEFAULT 'suggested' CHECK (status IN ('suggested', 'tracked', 'dismissed')),
    -- Who found it: manual, or the connector that suggested it.
    source           text NOT NULL DEFAULT 'manual',
    -- Keywords it shares with the site, by the suggesting connection.
    common_keywords  double precision,
    -- The company's customer record, when one has this domain.
    record_id        uuid REFERENCES records (id) ON DELETE SET NULL,
    -- From the latest light look: the home page's title and description,
    -- how many pages its sitemaps list, and why the look failed.
    title            text NOT NULL DEFAULT '',
    description      text NOT NULL DEFAULT '',
    pages            integer NOT NULL DEFAULT 0,
    checked_at       timestamptz,
    check_error      text,
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, host)
);
CREATE INDEX seo_competitors_ws_idx ON seo_competitors (workspace_id, site_id, status);
CREATE INDEX seo_competitors_due_idx ON seo_competitors (checked_at) WHERE status = 'tracked';

-- The pages a tracked competitor's sitemaps list, so new and removed pages
-- show between looks.
CREATE TABLE competitor_pages (
    competitor_id  uuid NOT NULL REFERENCES seo_competitors (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url            text NOT NULL,
    lastmod        text NOT NULL DEFAULT '',
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    -- Set when a look no longer finds it.
    gone_at        timestamptz,
    PRIMARY KEY (competitor_id, url)
);

-- What changed at a competitor: pages added or removed, a new home page
-- title or description, and moves in domain rating, traffic, keywords or
-- referring domains by an SEO connection. Rows are only ever added.
CREATE TABLE competitor_events (
    id             bigserial PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    competitor_id  uuid NOT NULL REFERENCES seo_competitors (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('pages_added', 'pages_removed', 'title_changed', 'description_changed',
                                                 'rating_changed', 'traffic_changed', 'keywords_changed', 'links_changed')),
    -- The connector whose numbers moved; '' for OutflareBot's look.
    connector      text NOT NULL DEFAULT '',
    -- Counts and sample URLs for pages, before and after for the rest.
    data           jsonb NOT NULL DEFAULT '{}',
    at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX competitor_events_site_idx ON competitor_events (workspace_id, site_id, at DESC);
