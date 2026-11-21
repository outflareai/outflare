-- Routing by working hours and capacity (docs/routing.md, "Hours and
-- capacity"): what routing knows about one teammate in a brand. A teammate
-- with no row is always at work and takes any number of records.
CREATE TABLE routing_people (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The most of the brand's people and companies they own at once; null
    -- is no limit. At the limit, routing gives them no more.
    capacity     integer CHECK (capacity BETWEEN 0 AND 1000000),
    -- The days they work, 1 (Monday) to 7 (Sunday); none means every day.
    days         smallint[] NOT NULL DEFAULT '{}',
    -- Their working hours as minutes after midnight in their time zone;
    -- both null means all day. An end before the start runs past midnight.
    from_minute  smallint CHECK (from_minute BETWEEN 0 AND 1439),
    to_minute    smallint CHECK (to_minute BETWEEN 0 AND 1440),
    timezone     text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 64),
    -- Away (on leave): routing gives them nothing until it is switched off.
    paused       boolean NOT NULL DEFAULT false,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, user_id),
    CHECK ((from_minute IS NULL) = (to_minute IS NULL))
);
CREATE INDEX routing_people_workspace ON routing_people (workspace_id);
CREATE INDEX routing_people_user ON routing_people (user_id);
