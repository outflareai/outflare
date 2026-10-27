-- Backlinks: the links other sites have to an SEO site, merged from every
-- SEO connection that reads links (Ahrefs, DataForSEO). One row is one
-- link (linking page to linked page), whichever providers saw it; each
-- provider's own view is kept beside it. A link's life is recorded as
-- events (new, lost, regained) that are never changed afterwards, so the
-- history stays when the providers forget.

CREATE TABLE backlinks (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- The site's brand, kept in step with it, for brand filters.
    brand_id       uuid REFERENCES brands (id) ON DELETE SET NULL,
    source_url     text NOT NULL,
    target_url     text NOT NULL,
    -- The linking site's host, lowercase, without www.
    source_domain  text NOT NULL,
    anchor         text NOT NULL DEFAULT '',
    nofollow       boolean NOT NULL DEFAULT false,
    sponsored      boolean NOT NULL DEFAULT false,
    ugc            boolean NOT NULL DEFAULT false,
    -- live while any provider still finds it, lost once none does.
    status         text NOT NULL DEFAULT 'live' CHECK (status IN ('live', 'lost')),
    -- The linking domain's strength, 0 to 100: Ahrefs DR when Ahrefs saw
    -- it, else DataForSEO's rank on a 0 to 100 scale.
    source_rating  double precision,
    -- When a provider first found it, when it was lost (by the providers'
    -- dates) and when it last came back.
    first_seen_at  timestamptz,
    lost_at        timestamptz,
    regained_at    timestamptz,
    -- Each provider's latest view, by connector id: status, dates, rating,
    -- spam judgement, lost reason, the run that saw it.
    providers      jsonb NOT NULL DEFAULT '{}',
    -- The last time a provider reported it.
    checked_at     timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, source_url, target_url)
);
CREATE INDEX backlinks_ws_idx ON backlinks (workspace_id, site_id, status, source_rating DESC NULLS LAST);
CREATE INDEX backlinks_domain_idx ON backlinks (workspace_id, source_domain);

-- One provider's read of one site's links: what it saw and cost.
CREATE TABLE backlink_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    connector      text NOT NULL,
    status         text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
    error          text,
    -- Dollars (DataForSEO) or API units (Ahrefs).
    cost           double precision NOT NULL DEFAULT 0,
    unit           text NOT NULL DEFAULT '',
    links_seen     integer NOT NULL DEFAULT 0,
    new_links      integer NOT NULL DEFAULT 0,
    lost_links     integer NOT NULL DEFAULT 0,
    regained_links integer NOT NULL DEFAULT 0,
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);
CREATE INDEX backlink_runs_site_idx ON backlink_runs (workspace_id, site_id, started_at DESC);

-- A link's life: new (first found), lost (no provider finds it) and
-- regained (found again). Rows are only ever added.
CREATE TABLE backlink_events (
    id             bigserial PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    backlink_id    uuid NOT NULL REFERENCES backlinks (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('new', 'lost', 'regained')),
    -- When it happened, by the provider's dates where it gives them.
    at             timestamptz NOT NULL,
    connector      text NOT NULL,
    run_id         uuid REFERENCES backlink_runs (id) ON DELETE SET NULL,
    -- The linking domain's rating then, and why a lost link was lost.
    source_rating  double precision,
    reason         text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX backlink_events_site_idx ON backlink_events (workspace_id, site_id, at DESC);
