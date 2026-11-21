-- What the last import of a site's tracked keywords from a connection's
-- own rank tracker (SE Ranking's projects) did, so the Rankings page can
-- say it: the project it matched and how many keywords came in, that no
-- project of the account is for this site (with the sites its projects are
-- for), or that the read failed and why. One row per site and connection,
-- replaced by each import. Before this an import that brought nothing in
-- left no trace anyone could see.
CREATE TABLE rank_imports (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    ran_at         timestamptz NOT NULL DEFAULT now(),
    -- imported: a project matched (keywords may still be 0). no_project:
    -- none of the account's projects is for this site. failed: the read
    -- failed; error_code says how.
    outcome        text NOT NULL CHECK (outcome IN ('imported', 'no_project', 'failed')),
    project        text NOT NULL DEFAULT '',
    keywords       integer NOT NULL DEFAULT 0,
    added          integer NOT NULL DEFAULT 0,
    days           integer NOT NULL DEFAULT 0,
    -- What could not be mapped (an engine whose country is unknown...).
    skipped        jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(skipped) = 'array'),
    -- The sites the account's projects are for, when none matched.
    projects_seen  jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(projects_seen) = 'array'),
    -- The kind of failure: auth, quota, unavailable, invalid...
    error_code     text NOT NULL DEFAULT '',
    PRIMARY KEY (site_id, integration_id)
);
CREATE INDEX rank_imports_ws_idx ON rank_imports (workspace_id, brand_id);
