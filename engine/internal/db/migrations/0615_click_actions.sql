-- Named clicks: a brand names a click it cares about (the "Start free"
-- button, any link to /signup) without changing its site. When the
-- website script's autocapture sends a matching Element Clicked event, the
-- same click is also stored as an event with the name the brand chose, so
-- it can be used in funnels, trends and A/B test goals like any event the
-- site sends itself.
CREATE TABLE click_actions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The event's name as the brand wrote it, such as Signup Clicked.
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- What a click must match; every one that is set must match.
    -- The label, compared without case: exactly, or as a part of it.
    label         text NOT NULL DEFAULT '' CHECK (length(label) <= 200),
    label_match   text NOT NULL DEFAULT 'exact' CHECK (label_match IN ('exact', 'contains')),
    -- A part of where a link goes.
    href_contains text NOT NULL DEFAULT '' CHECK (length(href_contains) <= 500),
    element_id    text NOT NULL DEFAULT '' CHECK (length(element_id) <= 200),
    -- One of the element's classes.
    class         text NOT NULL DEFAULT '' CHECK (length(class) <= 200),
    -- The page: a path such as /pricing, or with * for any run of characters.
    path          text NOT NULL DEFAULT '' CHECK (length(path) <= 200),
    enabled       boolean NOT NULL DEFAULT true,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX click_actions_name_idx ON click_actions (brand_id, lower(name));
CREATE INDEX click_actions_workspace_idx ON click_actions (workspace_id);
