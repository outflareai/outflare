-- Audiences: a brand names a group of people once (one of its lists) and
-- chooses which ad accounts it goes to, so the same audience reaches
-- several ad networks. A daily job keeps each network's customer list in
-- step with the list: people who joined and may be added go in with
-- hashed emails and phones only, people who left or withdrew consent come
-- out.
--
-- Expand only (docs/migrations.md): new tables the previous version never
-- reads.
CREATE TABLE ad_audiences (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- The list whose people the audience holds. Null once the list is
    -- deleted: the audience stops syncing and says so.
    list_id      uuid REFERENCES smart_lists (id) ON DELETE SET NULL,
    enabled      boolean NOT NULL DEFAULT true,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ad_audiences_name_idx ON ad_audiences (brand_id, lower(name));
CREATE INDEX ad_audiences_workspace_idx ON ad_audiences (workspace_id, brand_id);

-- One ad account an audience goes to, and how its last sync went.
CREATE TABLE ad_audience_destinations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    audience_id   uuid NOT NULL REFERENCES ad_audiences (id) ON DELETE CASCADE,
    ad_account_id uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    -- The customer list's id on the network; '' until the first sync
    -- makes it.
    external_id   text NOT NULL DEFAULT '' CHECK (length(external_id) <= 64),
    -- People Outflare has sent and not taken out; people in the list who
    -- could not go (no consent, nothing to match on).
    members       integer NOT NULL DEFAULT 0,
    skipped       integer NOT NULL DEFAULT 0,
    -- What the network says it matched (connectors.AudienceSize).
    size          jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(size) = 'object'),
    error         text NOT NULL DEFAULT '' CHECK (length(error) <= 300),
    request_id    text NOT NULL DEFAULT '' CHECK (length(request_id) <= 200),
    synced_at     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (audience_id, ad_account_id)
);

CREATE INDEX ad_audience_destinations_account_idx ON ad_audience_destinations (ad_account_id);
CREATE INDEX ad_audience_destinations_due_idx ON ad_audience_destinations (synced_at NULLS FIRST);

-- Who Outflare has sent to each destination, so a sync sends only what
-- changed and knows whom to take out.
CREATE TABLE ad_audience_members (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    destination_id uuid NOT NULL REFERENCES ad_audience_destinations (id) ON DELETE CASCADE,
    record_id      uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    added_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (destination_id, record_id)
);

CREATE INDEX ad_audience_members_record_idx ON ad_audience_members (record_id);
