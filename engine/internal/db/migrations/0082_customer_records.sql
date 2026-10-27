-- Customer records: the one model of people, companies and anything else a
-- workspace tracks (deals, subscriptions, products). The customer data
-- platform, the CRM and lifecycle email all read and write these tables, so
-- a fact about a person or company is stored once.
--
-- Built-in record types (person, account), built-in fields and built-in
-- option lists live in code (engine/internal/api/catalog.go); these tables
-- hold what a workspace adds on top.

-- A record type a workspace adds, such as deal, subscription or partner.
CREATE TABLE record_types (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Stable key used in the API and in field and event definitions: 2 to
    -- 40 of a-z, 0-9 and underscores. Never changes.
    key          text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
    name         text NOT NULL,
    plural       text NOT NULL,
    description  text NOT NULL DEFAULT '',
    archived_at  timestamptz,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, key)
);

-- A list of choices that select and status fields share, such as deal
-- stages or partner tiers, so the same choice is never typed twice.
CREATE TABLE option_sets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{1,39}$'),
    name         text NOT NULL,
    -- [{"key": "won", "label": "Won", "archived": false}], in order. Keys
    -- never change or go away; an option is archived instead.
    options      jsonb NOT NULL DEFAULT '[]',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, key)
);

-- A field a workspace adds to a record type: the one definition every
-- feature uses for that fact (imports, list filters, enrichment, the CRM,
-- product traits, merge tags).
CREATE TABLE field_definitions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_type   text NOT NULL,
    key           text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{1,62}$'),
    label         text NOT NULL,
    description   text NOT NULL DEFAULT '',
    value_type    text NOT NULL CHECK (value_type IN ('text', 'number', 'currency', 'date', 'timestamp', 'checkbox', 'select',
                      'status', 'rating', 'email', 'phone', 'url', 'location', 'record_reference', 'actor_reference')),
    -- select and status: the option list's key (built in or option_sets).
    option_set    text,
    -- Several values: select, email, phone, url and the references.
    multiple      boolean NOT NULL DEFAULT false,
    -- record_reference: the record type it points at.
    target_type   text,
    -- Who may change it: source_owned (only its source system),
    -- platform_owned (people and agents in Outflare), manual_override_allowed
    -- (a source, and people may override), computed (Outflare works it out).
    write_policy  text NOT NULL DEFAULT 'platform_owned'
                      CHECK (write_policy IN ('source_owned', 'platform_owned', 'manual_override_allowed', 'computed')),
    -- Which source wins when two disagree, first wins.
    precedence    text[] NOT NULL DEFAULT '{manual,first_party,crm_sync,enrichment}',
    sensitivity   text NOT NULL DEFAULT 'internal' CHECK (sensitivity IN ('public', 'internal', 'confidential', 'restricted')),
    -- Shown as a filter and sortable column, and indexed for it.
    filterable    boolean NOT NULL DEFAULT false,
    archived_at   timestamptz,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, record_type, key)
);

-- An event a workspace's product sends, such as product.activated. Names
-- seen at ingest that nobody has defined yet arrive as quarantined, and
-- only approved events can drive rules and journeys.
CREATE TABLE event_definitions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (name ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$' AND length(name) <= 100),
    label         text NOT NULL DEFAULT '',
    description   text NOT NULL DEFAULT '',
    -- [{"key": "product_id", "value_type": "text", "description": ""}]
    properties    jsonb NOT NULL DEFAULT '[]',
    status        text NOT NULL DEFAULT 'approved' CHECK (status IN ('quarantined', 'approved', 'deprecated')),
    sensitivity   text NOT NULL DEFAULT 'internal' CHECK (sensitivity IN ('public', 'internal', 'confidential', 'restricted')),
    first_seen_at timestamptz,
    last_seen_at  timestamptz,
    seen_count    bigint NOT NULL DEFAULT 0,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

-- One person, company or other tracked thing.
CREATE TABLE records (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_type  text NOT NULL,
    -- Null: shared by every brand.
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- What people see: a person's or company's name, a deal's title.
    name         text NOT NULL DEFAULT '',
    -- Current values by field key. History is in attribute_changes.
    attributes   jsonb NOT NULL DEFAULT '{}',
    -- Which source set each value, by field key (manual, first_party,
    -- crm_sync, enrichment, import), so a lower-precedence source never
    -- overwrites a higher one.
    attribute_sources jsonb NOT NULL DEFAULT '{}',
    -- Where the record came from: first_party (the workspace's own product,
    -- billing or forms), prospect (lead lists and data providers), crm_sync,
    -- import, or manual. Lifecycle mail never relies on it: consent does.
    origin       text NOT NULL DEFAULT 'manual' CHECK (origin IN ('first_party', 'prospect', 'crm_sync', 'import', 'manual')),
    -- Bumped on every change, so an edit made against an old copy fails.
    version      integer NOT NULL DEFAULT 1,
    -- Set when this record was merged into another.
    merged_into  uuid REFERENCES records (id) ON DELETE SET NULL,
    archived_at  timestamptz,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX records_list_idx ON records (workspace_id, record_type, created_at DESC, id DESC);
CREATE INDEX records_name_idx ON records (workspace_id, record_type, lower(name));
CREATE INDEX records_brand_idx ON records (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX records_merged_idx ON records (merged_into) WHERE merged_into IS NOT NULL;
CREATE INDEX records_attributes_idx ON records USING gin (attributes jsonb_path_ops);

-- A key that points at a record: an email, phone, device token, the
-- workspace's own user id, a Stripe customer, an anonymous browser id, a
-- company domain, a lead. Closed identities stay as history.
CREATE TABLE record_identities (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- email, phone, device_token, user_id, anonymous_id, domain,
    -- outflare.lead, or a source's own (stripe.customer, app.user).
    namespace     text NOT NULL CHECK (namespace ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$' AND length(namespace) <= 60),
    -- Normalised: emails and domains lower case, phones in E.164.
    value         text NOT NULL CHECK (length(value) BETWEEN 1 AND 500),
    -- Only a verified identity claims its value: an unconfirmed sign-up with
    -- someone else's email never takes over that person's record.
    verified      boolean NOT NULL DEFAULT false,
    verified_at   timestamptz,
    -- device_token: {"platform": "ios"}. Never secrets.
    properties    jsonb NOT NULL DEFAULT '{}',
    -- Who said so: api, import, manual, or a connector's id.
    source        text NOT NULL DEFAULT 'api',
    valid_from    timestamptz NOT NULL DEFAULT now(),
    valid_to      timestamptz,
    -- changed (the person changed it), merged, removed, claimed (a verified
    -- owner took it).
    closed_reason text CHECK (closed_reason IN ('changed', 'merged', 'removed', 'claimed')),
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- One verified owner per value: the send-time lookup (policy checks) and
-- identity resolution both use these.
CREATE UNIQUE INDEX record_identities_owner_idx ON record_identities (workspace_id, namespace, value)
    WHERE valid_to IS NULL AND verified;
CREATE INDEX record_identities_lookup_idx ON record_identities (workspace_id, namespace, value) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX record_identities_once_idx ON record_identities (record_id, namespace, value) WHERE valid_to IS NULL;
CREATE INDEX record_identities_record_idx ON record_identities (record_id, valid_from DESC);

-- A typed link between two records: a person member_of an account, a deal
-- for an account. Record reference fields are kept here too, with their
-- field key, so there is one graph.
CREATE TABLE record_relationships (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    from_id      uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    to_id        uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{1,62}$'),
    -- Set when the link is a record reference field's value.
    field_key    text,
    properties   jsonb NOT NULL DEFAULT '{}',
    valid_from   timestamptz NOT NULL DEFAULT now(),
    valid_to     timestamptz,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (from_id <> to_id)
);

CREATE UNIQUE INDEX record_relationships_active_idx ON record_relationships (from_id, to_id, kind) WHERE valid_to IS NULL;
CREATE INDEX record_relationships_to_idx ON record_relationships (to_id) WHERE valid_to IS NULL;

-- Every change to a record's values, newest last, so a profile shows what
-- a field was, who changed it and from which source.
CREATE TABLE attribute_changes (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- A field key, or name for the record's name.
    field_key    text NOT NULL,
    old_value    jsonb,
    new_value    jsonb,
    source       text NOT NULL DEFAULT 'manual',
    actor_id     uuid REFERENCES actors (id) ON DELETE SET NULL,
    request_id   text NOT NULL DEFAULT '',
    changed_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attribute_changes_record_idx ON attribute_changes (record_id, changed_at DESC);
