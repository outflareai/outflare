-- Feature flags: a switch or a split a workspace's site and product read
-- to decide what a visitor sees. A flag is on or off; when on, each
-- visitor who passes its rules and rollout gets one of its variants by
-- weight. The variant comes from a hash of the flag's salt and the
-- visitor's id, so it is the same on every page and every server, with
-- nothing stored per visitor. Experiments (A/B tests) build on these.

CREATE TABLE flags (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand whose site reads it; null is shared by every brand.
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- What code asks for: outflare.flag("new-pricing").
    key          text NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'off' CHECK (status IN ('on', 'off', 'archived')),
    -- [{"key": "control", "weight": 50, "payload": {...}}, ...]; a plain
    -- switch has one variant, "on", at 100.
    variants     jsonb NOT NULL,
    -- The share of visitors who pass the rules that get a variant at all.
    rollout      smallint NOT NULL DEFAULT 100 CHECK (rollout BETWEEN 0 AND 100),
    -- Conditions every one of which must hold: [{"attribute": "path",
    -- "op": "starts_with", "values": ["/pricing"]}].
    rules        jsonb NOT NULL DEFAULT '[]',
    -- Whether the website script gets it. A flag only servers read stays
    -- out of the page.
    on_site      boolean NOT NULL DEFAULT true,
    -- Mixed into the hash, so two flags split visitors independently.
    salt         text NOT NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    archived_at  timestamptz
);
-- A key names one live flag; an archived flag frees its key.
CREATE UNIQUE INDEX flags_key_idx ON flags (workspace_id, key) WHERE status <> 'archived';
CREATE INDEX flags_ws_idx ON flags (workspace_id, status);
