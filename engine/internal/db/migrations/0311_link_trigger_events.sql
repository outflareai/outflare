-- Events that can start link building playbooks (playbook automations).
--
-- outbound_link.broken: pages of broken-link prospects nobody has pitched
-- yet are checked again every recheck_days (0 is never), so a link that
-- breaks later is found. A check is free apart from the workspace's own AI
-- reading the page.
ALTER TABLE link_building_settings ADD COLUMN recheck_days int NOT NULL DEFAULT 30 CHECK (recheck_days BETWEEN 0 AND 365);

-- asset.published: a page of the workspace's own worth promoting (a guide,
-- a study, a tool), marked published by a person or an agent.
CREATE TABLE link_assets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id      uuid REFERENCES seo_sites (id) ON DELETE SET NULL,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    url          text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    title        text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    topic        text NOT NULL DEFAULT '' CHECK (length(topic) <= 300),
    published_at timestamptz NOT NULL DEFAULT now(),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX link_assets_url_idx ON link_assets (workspace_id, url);
CREATE INDEX link_assets_ws_idx ON link_assets (workspace_id, published_at DESC);
