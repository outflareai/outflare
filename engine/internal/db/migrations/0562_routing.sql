-- Routing: which teammates get which people and companies. A brand keeps
-- an ordered list of rules; the first one whose conditions hold for a
-- record with no owner gives it to the member of its pool who owns the
-- fewest records in the brand, so work is spread evenly.
CREATE TABLE routing_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    -- Rules are tried in this order; the first that matches wins.
    position     integer NOT NULL DEFAULT 0,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    record_type  text NOT NULL CHECK (record_type IN ('person', 'account')),
    -- The same attribute conditions rules and scores use; none means
    -- every record of the type (a catch-all).
    conditions   jsonb NOT NULL DEFAULT '[]',
    match        text NOT NULL DEFAULT 'all' CHECK (match IN ('all', 'any')),
    -- The pool: user ids of workspace members.
    members      uuid[] NOT NULL DEFAULT '{}',
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    -- How many records it has given an owner.
    assigned     bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX routing_rules_brand ON routing_rules (brand_id, position);
CREATE INDEX routing_rules_workspace ON routing_rules (workspace_id);

-- How far the background round has looked through a brand's records
-- with no owner. Empty after the rules change, so every one is looked at
-- again.
CREATE TABLE routing_state (
    brand_id      uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    checked_until timestamptz,
    -- The last record looked at, for records changed at the same moment.
    checked_id    uuid,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX routing_state_workspace ON routing_state (workspace_id);
