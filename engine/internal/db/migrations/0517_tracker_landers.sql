-- Landers (plans/ad-tracker.md, T6b): pages a brand sends tracker clicks to
-- before its offers, such as a review or a quiz, whose buttons go on to an
-- offer through /click (by weight) or /click/N (the Nth offer). A link's
-- destination can be a lander; a list of destinations with landers sends a
-- click to one of its landers, and the lander click to one of its other
-- destinations. Each lander click is kept, with the offer it went to. Only
-- adds: the version before routes a lander like any other page.
CREATE TABLE tracker_landers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    url          text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    -- How many calls to action it has (/click/1 to /click/N); each has its
    -- own click URL.
    ctas         integer NOT NULL DEFAULT 1 CHECK (ctas BETWEEN 1 AND 100),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tracker_landers_name_idx ON tracker_landers (workspace_id, brand_id, lower(name));

-- A destination that is a lander. Its url is the lander's, updated with it.
ALTER TABLE tracking_link_destinations ADD COLUMN lander_id uuid REFERENCES tracker_landers (id) ON DELETE SET NULL;
CREATE INDEX tracking_link_destinations_lander_idx ON tracking_link_destinations (lander_id) WHERE lander_id IS NOT NULL;

-- The lander a click went to. Not a foreign key, as the click table is large.
ALTER TABLE tracking_link_clicks ADD COLUMN lander_id uuid;

-- Each click on a lander's call to action: the click it continues, which
-- call to action (0 for /click), and where it went.
CREATE TABLE tracker_lander_clicks (
    id             bigserial PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    lander_id      uuid REFERENCES tracker_landers (id) ON DELETE SET NULL,
    -- The redirect click (tracking_link_clicks.id; not a foreign key, as
    -- that table is large) and its key.
    click_id       bigint NOT NULL,
    click_key      text NOT NULL CHECK (length(click_key) <= 32),
    cta            integer NOT NULL DEFAULT 0 CHECK (cta BETWEEN 0 AND 100),
    destination_id uuid,
    offer_id       uuid,
    likely_bot     boolean NOT NULL DEFAULT false,
    clicked_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_lander_clicks_brand_idx ON tracker_lander_clicks (workspace_id, brand_id, clicked_at DESC);
CREATE INDEX tracker_lander_clicks_lander_idx ON tracker_lander_clicks (lander_id, clicked_at) WHERE lander_id IS NOT NULL;
CREATE INDEX tracker_lander_clicks_key_idx ON tracker_lander_clicks (click_key);
