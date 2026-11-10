-- Weighted destinations and daily click caps on a redirect link
-- (plans/ad-tracker.md, T4a). A link can send its clicks to several pages,
-- each chosen in proportion to its weight, and stop sending to one once it
-- has had its clicks for the day (a cap of 0 stops it now). When every
-- destination is capped or stopped, clicks go to the link's own page: the
-- fallback. Only adds, so the version before still works (it sends every
-- click to the link's own page).
CREATE TABLE tracking_link_destinations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    link_id         uuid NOT NULL REFERENCES tracking_links (id) ON DELETE CASCADE,
    -- What reports call it; '' shows the page.
    name            text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
    url             text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    weight          integer NOT NULL DEFAULT 100 CHECK (weight BETWEEN 0 AND 10000),
    -- Clicks a day it takes; NULL for no cap, 0 to stop it.
    daily_click_cap integer CHECK (daily_click_cap >= 0),
    position        integer NOT NULL DEFAULT 0,
    -- The rule-based path it belongs to (T4b); NULL is the link's default
    -- destinations, the only ones this version routes to.
    path_id         uuid,
    -- A destination taken off the link is kept, so its clicks still say
    -- where they went.
    removed_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracking_link_destinations_link_idx ON tracking_link_destinations (link_id, position);

-- Clicks each destination had on a day (the brand's day; UTC until brands
-- have a reporting time zone), counted by the servers as they write their
-- click batches, so a cap holds across servers.
CREATE TABLE tracking_destination_days (
    destination_id uuid NOT NULL REFERENCES tracking_link_destinations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    day            date NOT NULL,
    clicks         bigint NOT NULL DEFAULT 0 CHECK (clicks >= 0),
    PRIMARY KEY (destination_id, day)
);
CREATE INDEX tracking_destination_days_day_idx ON tracking_destination_days (day);

-- Which destination a click went to; NULL for the link's own page. Not a
-- foreign key: destinations are kept when taken off a link, and a key would
-- make deleting a link scan every click.
ALTER TABLE tracking_link_clicks ADD COLUMN destination_id uuid;
