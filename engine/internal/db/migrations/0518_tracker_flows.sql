-- Flows (plans/ad-tracker.md, T6c): a brand's reusable set of rule-based
-- paths and default destinations (pages, offers and landers), shared by
-- any number of campaign links, so one edit changes every link that uses
-- it. A flow's paths and destinations are rows of the same tables as a
-- link's, owned by the flow instead of a link. A link with a flow routes by
-- the flow's, not its own. Only adds and loosens: the version before reads
-- only rows with a link_id, and routes a link with a flow by its own
-- destinations, which still exist.
CREATE TABLE tracker_flows (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tracker_flows_name_idx ON tracker_flows (workspace_id, brand_id, lower(name));

-- A path or destination belongs to a link or to a flow (Outflare sets one;
-- the check only asks for at least one).
ALTER TABLE tracking_link_paths ALTER COLUMN link_id DROP NOT NULL;
ALTER TABLE tracking_link_paths ADD COLUMN flow_id uuid REFERENCES tracker_flows (id) ON DELETE CASCADE;
ALTER TABLE tracking_link_paths ADD CONSTRAINT tracking_link_paths_owner CHECK (link_id IS NOT NULL OR flow_id IS NOT NULL);
CREATE INDEX tracking_link_paths_flow_idx ON tracking_link_paths (flow_id, position) WHERE flow_id IS NOT NULL;

ALTER TABLE tracking_link_destinations ALTER COLUMN link_id DROP NOT NULL;
ALTER TABLE tracking_link_destinations ADD COLUMN flow_id uuid REFERENCES tracker_flows (id) ON DELETE CASCADE;
ALTER TABLE tracking_link_destinations ADD CONSTRAINT tracking_link_destinations_owner CHECK (link_id IS NOT NULL OR flow_id IS NOT NULL);
CREATE INDEX tracking_link_destinations_flow_idx ON tracking_link_destinations (flow_id, position) WHERE flow_id IS NOT NULL;

-- The flow a link routes by; NULL for its own paths and destinations.
ALTER TABLE tracking_links ADD COLUMN flow_id uuid REFERENCES tracker_flows (id) ON DELETE SET NULL;
CREATE INDEX tracking_links_flow_idx ON tracking_links (flow_id) WHERE flow_id IS NOT NULL;
