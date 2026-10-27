-- Links found for a brand: its social profiles and app store listings,
-- read from the brand's own website through the workspace's own page
-- reader (Geonode). A person adds each one to the brand's links (edited
-- if need be) or dismisses it; a dismissed link is not suggested again.
CREATE TABLE brand_link_suggestions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- A brand link kind: x, linkedin, youtube, app_store...
    kind         text NOT NULL,
    url          text NOT NULL,
    -- The page it was found on.
    found_on     text NOT NULL DEFAULT '',
    -- suggested: waiting for a person. added: now one of the brand's
    -- links. dismissed: not the brand's, never suggested again.
    status       text NOT NULL DEFAULT 'suggested' CHECK (status IN ('suggested', 'added', 'dismissed')),
    decided_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, url)
);
CREATE INDEX brand_link_suggestions_brand ON brand_link_suggestions (workspace_id, brand_id, status);
