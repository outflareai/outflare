-- Help centres: the knowledge marked for the help centre (visibility
-- public, published), as a public site customers read themselves. One per
-- brand (or one for the whole workspace), off until turned on. It is
-- served at /h/<slug>/ on this server, or on a domain of the workspace's
-- own that points here (a tracking domain tied to it, like a partner
-- portal's). settings holds its look, layout and options; the server
-- fills in defaults, so an empty object is a good-looking help centre.
CREATE TABLE help_centres (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand whose articles it shows; null for the whole workspace.
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    slug         text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    enabled      boolean NOT NULL DEFAULT false,
    domain_id    uuid UNIQUE REFERENCES tracking_domains (id) ON DELETE SET NULL,
    settings     jsonb NOT NULL DEFAULT '{}',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX help_centres_brand ON help_centres (workspace_id, coalesce(brand_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- A folder's icon on the help centre (a name from a fixed set).
ALTER TABLE knowledge_collections ADD COLUMN icon text NOT NULL DEFAULT '' CHECK (icon ~ '^[a-z-]{0,30}$');

-- Daily counts for each article read on a help centre: views, and the
-- answers to "Did this answer your question?". Counts only: nothing about
-- the reader is kept.
CREATE TABLE help_article_days (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    item_id      uuid NOT NULL REFERENCES knowledge_items (id) ON DELETE CASCADE,
    day          date NOT NULL,
    views        integer NOT NULL DEFAULT 0,
    helpful      integer NOT NULL DEFAULT 0,
    unhelpful    integer NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, day)
);
CREATE INDEX help_article_days_workspace ON help_article_days (workspace_id, day);

-- Daily counts for a help centre as a whole: page views, searches and the
-- searches that found nothing (those questions also join the knowledge's
-- unanswered questions).
CREATE TABLE help_centre_days (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    centre_id      uuid NOT NULL REFERENCES help_centres (id) ON DELETE CASCADE,
    day            date NOT NULL,
    views          integer NOT NULL DEFAULT 0,
    searches       integer NOT NULL DEFAULT 0,
    empty_searches integer NOT NULL DEFAULT 0,
    PRIMARY KEY (centre_id, day)
);
CREATE INDEX help_centre_days_workspace ON help_centre_days (workspace_id, day);
