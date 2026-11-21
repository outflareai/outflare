-- The website a visit was recorded on (its host name, such as
-- acme.example or app.acme.example), so recordings can be filtered by
-- site when a brand's snippet is on more than one. Empty for recordings
-- made before this.
ALTER TABLE session_recordings ADD COLUMN host text NOT NULL DEFAULT '' CHECK (length(host) <= 255);

-- A picture of a page for its heatmap: the page's document as one
-- visitor's browser showed it (the recorder's first two events, gzipped),
-- with the same masking as recordings and no visitor id. One per page and
-- kind of device, replaced when it is a week old, so heatmaps show the
-- page behind the clicks without session recordings being on.
CREATE TABLE heatmap_snapshots (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    path         text NOT NULL CHECK (length(path) BETWEEN 1 AND 500),
    device       text NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'tablet', 'mobile')),
    width        integer NOT NULL DEFAULT 0,
    body         bytea NOT NULL CHECK (octet_length(body) <= 2097152),
    taken_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, path, device)
);
CREATE INDEX heatmap_snapshots_workspace_idx ON heatmap_snapshots (workspace_id);
