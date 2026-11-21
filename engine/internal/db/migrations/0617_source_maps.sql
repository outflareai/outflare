-- Source maps: a brand uploads the map its build made for each file it
-- serves, and the Errors page reads every stack through them, so an error
-- in minified code shows the brand's own file, line and function. A map
-- is kept gzipped and without the source code it may carry: names and
-- positions are all a stack needs.
CREATE TABLE source_maps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The built file as the site serves it, such as /assets/app-3f9a1c.js.
    file         text NOT NULL CHECK (length(file) BETWEEN 1 AND 500),
    -- The brand's own name for the build, or '' for none.
    release      text NOT NULL DEFAULT '' CHECK (length(release) <= 100),
    map          bytea NOT NULL,
    -- How many original files it names, and its size before gzip.
    sources      integer NOT NULL DEFAULT 0,
    bytes        integer NOT NULL DEFAULT 0,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX source_maps_file_idx ON source_maps (brand_id, file, release);
CREATE INDEX source_maps_workspace_idx ON source_maps (workspace_id);
