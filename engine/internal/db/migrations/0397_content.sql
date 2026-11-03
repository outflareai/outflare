-- Content: posts and pages written in Outflare for a brand's own website
-- (by a person, or drafted by an agent and approved), published through
-- the workspace's own CMS connection (connectors.CMSPublisher: WordPress,
-- Ghost). The body is Markdown; the publish job renders it to HTML with
-- raw HTML left out. Each item remembers the CMS post it became, so a
-- later change updates it there instead of making a second one.
CREATE TABLE content_items (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    -- The address slug; empty lets the CMS choose one from the title.
    slug           text NOT NULL DEFAULT '' CHECK (length(slug) <= 200),
    -- posts or pages.
    target         text NOT NULL DEFAULT 'posts' CHECK (target IN ('posts', 'pages')),
    body           text NOT NULL DEFAULT '' CHECK (length(body) <= 200000),
    excerpt        text NOT NULL DEFAULT '' CHECK (length(excerpt) <= 1000),
    tags           text[] NOT NULL DEFAULT '{}',
    canonical_url  text NOT NULL DEFAULT '' CHECK (length(canonical_url) <= 2000),
    -- draft: being written; approved: a person said it may go live;
    -- published: live on the CMS; archived: kept but not for publishing.
    status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved', 'published', 'archived')),
    -- Where it was published: the CMS connection and the post there.
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    external_id    text NOT NULL DEFAULT '' CHECK (length(external_id) <= 200),
    url            text NOT NULL DEFAULT '' CHECK (length(url) <= 2000),
    -- draft or published, as the CMS last showed it; empty before it is there.
    cms_status     text NOT NULL DEFAULT '' CHECK (cms_status IN ('', 'draft', 'published')),
    published_at   timestamptz,
    -- Notes for the team and the agent: the brief, the sources, what to check.
    notes          text NOT NULL DEFAULT '' CHECK (length(notes) <= 5000),
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX content_items_brand_idx ON content_items (brand_id, status, updated_at DESC);
CREATE INDEX content_items_workspace_idx ON content_items (workspace_id, created_at DESC);

-- A publication: one request to put an item on the CMS (as a draft there
-- or live), carried out by the content.publish job, which reads the post
-- back and records what the CMS shows.
CREATE TABLE content_publishes (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    item_id        uuid NOT NULL REFERENCES content_items (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- create: a new post on the CMS; update: the post it already is.
    kind           text NOT NULL CHECK (kind IN ('create', 'update')),
    live           boolean NOT NULL DEFAULT false,
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'applied', 'failed')),
    error          text NOT NULL DEFAULT '' CHECK (length(error) <= 500),
    -- What the CMS showed when read back after the change.
    external_id    text NOT NULL DEFAULT '',
    url            text NOT NULL DEFAULT '',
    cms_status     text NOT NULL DEFAULT '',
    verified       boolean,
    requested_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    applied_at     timestamptz
);

CREATE INDEX content_publishes_item_idx ON content_publishes (item_id, created_at DESC);
CREATE INDEX content_publishes_brand_idx ON content_publishes (brand_id, created_at DESC);
