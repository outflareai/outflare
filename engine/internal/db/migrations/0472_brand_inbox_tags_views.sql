-- Brands are fully separate (plans/brands/README.md): the inbox's tags
-- and saved views are each brand's own, the owner's decision of
-- 2026-11-05. Each brand starts with a copy of its workspace's; the first
-- brand's copies keep their ids, so links to them keep working.
--
-- Expand only (docs/migrations.md): inbox_tags and inbox_views stay for
-- the previous version, which still reads and writes them; a later
-- release drops them.

-- A brand's tags. A reply's tags column holds tag names; this table gives
-- them colours and a list to pick from.
CREATE TABLE brand_inbox_tags (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL, -- lowercase, as stored on replies
    color        text NOT NULL DEFAULT 'gray',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name)
);

CREATE INDEX brand_inbox_tags_ws_idx ON brand_inbox_tags (workspace_id);

INSERT INTO brand_inbox_tags (id, brand_id, workspace_id, name, color, created_at)
SELECT CASE WHEN b.id = outflare_first_brand(t.workspace_id) THEN t.id ELSE gen_random_uuid() END,
       b.id, t.workspace_id, t.name, t.color, t.created_at
FROM inbox_tags t JOIN brands b ON b.workspace_id = t.workspace_id
ON CONFLICT DO NOTHING;

-- A brand's saved inbox filters. user_id NULL: shared with the team.
CREATE TABLE brand_inbox_views (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid REFERENCES users (id) ON DELETE CASCADE,
    name         text NOT NULL,
    filters      jsonb NOT NULL DEFAULT '{}',
    position     integer NOT NULL DEFAULT 0,
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX brand_inbox_views_brand_idx ON brand_inbox_views (brand_id, position);
CREATE INDEX brand_inbox_views_ws_idx ON brand_inbox_views (workspace_id);

INSERT INTO brand_inbox_views (id, brand_id, workspace_id, user_id, name, filters, position, created_by, created_at, updated_at)
SELECT CASE WHEN b.id = outflare_first_brand(v.workspace_id) THEN v.id ELSE gen_random_uuid() END,
       b.id, v.workspace_id, v.user_id, v.name, v.filters, v.position, v.created_by, v.created_at, v.updated_at
FROM inbox_views v JOIN brands b ON b.workspace_id = v.workspace_id;
