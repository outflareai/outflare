-- Smart lists: groups of people on customer records that keep themselves
-- up to date from rules, such as "paid in the last 90 days" or "signed up
-- but never set up". Lead lists hold the leads someone picked or a search
-- found; a smart list holds whoever matches its rules right now, and
-- Outflare adds and removes people as their records and events change.
-- Email broadcasts, journeys, outreach and ad audiences read the same
-- members, so nobody builds the same group twice.
CREATE TABLE smart_lists (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: people of every brand. A brand's list holds that brand's
    -- people and those shared by every brand.
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    -- The tests a person must pass, in the shape smartlists.Rule
    -- describes, and whether all or any must hold.
    rules        jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(rules) = 'array'),
    match        text NOT NULL DEFAULT 'all' CHECK (match IN ('all', 'any')),
    -- How many people matched at the last refresh, and when it ran.
    members      integer NOT NULL DEFAULT 0,
    refreshed_at timestamptz,
    -- Why the last refresh failed, as an error code, or null.
    error_code   text,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX smart_lists_name_idx ON smart_lists (workspace_id, lower(name));
CREATE INDEX smart_lists_refresh_idx ON smart_lists (refreshed_at NULLS FIRST);

-- Who is in each smart list now, and since when. A refresh adds people
-- who match and removes people who no longer do, so this is always the
-- current membership; the times say when each person joined.
CREATE TABLE smart_list_members (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    list_id      uuid NOT NULL REFERENCES smart_lists (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    entered_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, record_id)
);

CREATE INDEX smart_list_members_record_idx ON smart_list_members (record_id);
CREATE INDEX smart_list_members_entered_idx ON smart_list_members (list_id, entered_at DESC, record_id);
