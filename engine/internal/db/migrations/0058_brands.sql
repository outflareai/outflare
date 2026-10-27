-- Brands: the businesses one company markets, such as Geonode, Repocket and
-- each of its games, each with its own website. A workspace holds many
-- brands so connectors, members, API tokens, leads and suppressions are set
-- up once and shared. A brand owns its context, memories, projects and
-- sending setup. Records without a brand are shared by every brand, and a
-- workspace with no brands works exactly as before.
CREATE TABLE brands (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    -- Short name for URLs and API calls (?brand=repocket): 2 to 40 of
    -- a-z, 0-9 and dashes.
    handle       text NOT NULL,
    -- The brand's main website, the point of truth for what it is.
    website      text NOT NULL DEFAULT '',
    -- Every domain that belongs to the brand, the website's included. The
    -- analytics and SEO reports for these domains are the brand's.
    domains      text[] NOT NULL DEFAULT '{}',
    -- Where the brand ships: web, ios, android, tv, desktop, console, api.
    platforms    text[] NOT NULL DEFAULT '{}',
    -- Where else the brand lives: app stores, socials, docs, as
    -- [{"kind": "app_store", "url": "https://...", "label": ""}].
    links        jsonb NOT NULL DEFAULT '[]',
    -- archived: hidden from pickers, its records kept for reporting.
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX brands_handle_idx ON brands (workspace_id, handle);
CREATE UNIQUE INDEX brands_name_idx ON brands (workspace_id, lower(name));

-- What is true for one brand. The company context (workspace_context) is
-- true for every brand; agents read both, rules adding up and the brand's
-- other fields winning where set.
CREATE TABLE brand_context (
    brand_id     uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- What the brand is and does.
    about        text NOT NULL DEFAULT '',
    -- Who it is for.
    audience     text NOT NULL DEFAULT '',
    goals        text NOT NULL DEFAULT '',
    voice        text NOT NULL DEFAULT '',
    -- Added to the company's rules, never replacing them.
    rules        text NOT NULL DEFAULT '',
    notes        text NOT NULL DEFAULT '',
    version      integer NOT NULL DEFAULT 1,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL
);

-- The company-wide knowledge of how the brands relate to each other.
ALTER TABLE workspace_context ADD COLUMN brands text NOT NULL DEFAULT '';

ALTER TABLE projects ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE sending_domains ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE mailboxes ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE lead_lists ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE sequences ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE missions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE memories ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE CASCADE;

CREATE INDEX projects_brand_idx ON projects (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX sending_domains_brand_idx ON sending_domains (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX mailboxes_brand_idx ON mailboxes (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX lead_lists_brand_idx ON lead_lists (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX sequences_brand_idx ON sequences (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX missions_brand_idx ON missions (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX memories_brand_idx ON memories (brand_id) WHERE brand_id IS NOT NULL;

-- A record in a project always has the project's brand, so the brand is
-- set once, on the project. A record leaving its project keeps the brand.
CREATE FUNCTION brand_from_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.project_id IS NOT NULL THEN
        SELECT brand_id INTO NEW.brand_id FROM projects WHERE id = NEW.project_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER sending_domains_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON sending_domains
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();
CREATE TRIGGER mailboxes_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON mailboxes
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();
CREATE TRIGGER lead_lists_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON lead_lists
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();
CREATE TRIGGER sequences_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON sequences
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();
CREATE TRIGGER missions_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON missions
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();
CREATE TRIGGER memories_brand BEFORE INSERT OR UPDATE OF project_id, brand_id ON memories
    FOR EACH ROW EXECUTE FUNCTION brand_from_project();

-- Moving a project to another brand moves everything in it.
CREATE FUNCTION project_brand_moved() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE sending_domains SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    UPDATE mailboxes SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    UPDATE lead_lists SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    UPDATE sequences SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    UPDATE missions SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    UPDATE memories SET brand_id = NEW.brand_id WHERE project_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER projects_brand_moved AFTER UPDATE OF brand_id ON projects
    FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id) EXECUTE FUNCTION project_brand_moved();

-- Which brand an analytics site reports for. A tool can read many sites
-- (GA4 properties, Search Console sites, PostHog projects); each one
-- belongs to the brand whose domains include its domain, unless a row
-- here says otherwise. A row with no brand_id makes the site shared.
CREATE TABLE analytics_site_brands (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site            text NOT NULL,
    brand_id        uuid REFERENCES brands (id) ON DELETE CASCADE,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, site)
);
CREATE INDEX analytics_site_brands_ws_idx ON analytics_site_brands (workspace_id);

-- analytics_site_brand is the brand a site reports for: the row above when
-- there is one, else the oldest active brand with the site's domain (or a
-- parent domain) among its domains.
CREATE FUNCTION analytics_site_brand(ws uuid, integration uuid, site_id text, host text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN o.integration_id IS NOT NULL THEN o.brand_id ELSE (
        SELECT b.id FROM brands b, unnest(b.domains) d
        WHERE b.workspace_id = ws AND b.status = 'active' AND host <> '' AND (host = d OR host LIKE '%.' || d)
        ORDER BY length(d) DESC, b.created_at LIMIT 1
    ) END
    FROM (SELECT 1) one
    LEFT JOIN analytics_site_brands o ON o.integration_id = integration AND o.site = site_id
$$;
