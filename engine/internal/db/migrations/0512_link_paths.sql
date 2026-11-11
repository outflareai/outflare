-- Rule-based paths on a redirect link (plans/ad-tracker.md, T4b). A path
-- has conditions on the click (country, device, network, IP range, hour,
-- first or returning visitor, custom fields...) and its own weighted
-- destinations. A click takes the first path, in order, whose conditions
-- all match and that has an open destination; otherwise the link's default
-- destinations (path_id NULL), then the link's own page. Only adds, so the
-- version before still works: it routes to the default destinations only.
CREATE TABLE tracking_link_paths (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    link_id      uuid NOT NULL REFERENCES tracking_links (id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
    -- [{"field": "country", "op": "is" | "is_not", "values": ["US", "CA"]}]:
    -- every condition must match, and a condition matches any of its values.
    conditions   jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(conditions) = 'array'),
    position     integer NOT NULL DEFAULT 0,
    active       boolean NOT NULL DEFAULT true,
    -- A path taken off the link is kept, so its clicks still say where they
    -- went.
    removed_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracking_link_paths_link_idx ON tracking_link_paths (link_id, position);

ALTER TABLE tracking_link_destinations
    ADD CONSTRAINT tracking_link_destinations_path_fkey FOREIGN KEY (path_id) REFERENCES tracking_link_paths (id) ON DELETE CASCADE;
CREATE INDEX tracking_link_destinations_path_idx ON tracking_link_destinations (path_id) WHERE path_id IS NOT NULL;

-- Which path a click took; NULL for the default destinations or the link's
-- own page. Not a foreign key, as with destination_id.
ALTER TABLE tracking_link_clicks ADD COLUMN path_id uuid;
