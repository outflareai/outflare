-- Products and lifecycle stages per product (plans/customer-data-platform,
-- step 4; decided by the owner on 2026-11-01).
--
-- A brand sells products. Events say which product they are about
-- (customer_events.product_id), found from the product's id or key, or
-- from an alias: a name the brand's own product or billing system uses
-- for it. A name nobody mapped yet is held for review and drives nothing.
--
-- Each company and person moves through stages per product (lead, trial,
-- active, at risk, churned), on two levels: billing stages on the
-- company, usage stages on each person. Moves are set per brand, and
-- each move is an event on the record's timeline that rules and journeys
-- can start on. A company's overall status is the best of its product
-- stages, plus flags such as partly churned.
--
-- Greenfield: stages start empty when a brand turns this on and fill only
-- from events after that. Nothing is rebuilt from history.

-- The products a brand sells.
CREATE TABLE products (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- A stable key events can use instead of the id, such as residential.
    -- Renaming the product never changes it.
    key           text NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9_.-]{0,62}$'),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- What makes a customer churned: a cancellation (subscription.canceled
    -- and the like) or no usage of the product in churn_days.
    churn_mode    text NOT NULL DEFAULT 'cancellation' CHECK (churn_mode IN ('cancellation', 'inactivity')),
    -- No usage in this many days moves an active customer to at risk.
    at_risk_days  integer NOT NULL DEFAULT 14 CHECK (at_risk_days BETWEEN 1 AND 365),
    -- With churn_mode inactivity: no usage in this many days is churned.
    churn_days    integer NOT NULL DEFAULT 30 CHECK (churn_days BETWEEN 1 AND 730),
    archived_at   timestamptz,
    version       integer NOT NULL DEFAULT 1,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, key)
);
CREATE INDEX products_workspace_idx ON products (workspace_id, brand_id);

-- Names a brand's events or billing system use for its products. A held
-- name was seen but not mapped yet; an ignored one is not a product.
CREATE TABLE product_aliases (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    product_id    uuid REFERENCES products (id) ON DELETE CASCADE,
    -- Where the name appears: product events (properties.product) or a
    -- billing connection (a subscription's product id).
    source        text NOT NULL CHECK (source IN ('event', 'billing')),
    value         text NOT NULL CHECK (length(value) BETWEEN 1 AND 200),
    status        text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'mapped', 'ignored')),
    seen_count    bigint NOT NULL DEFAULT 0,
    first_seen_at timestamptz,
    last_seen_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- A mapped name has product_id, and only a mapped one (the API checks).
    UNIQUE (brand_id, source, value)
);
CREATE INDEX product_aliases_product_idx ON product_aliases (product_id) WHERE product_id IS NOT NULL;
CREATE INDEX product_aliases_held_idx ON product_aliases (workspace_id, brand_id) WHERE status = 'held';

-- The product an event is about. No foreign key: events are archived, and
-- a product is archived rather than deleted.
ALTER TABLE customer_events ADD COLUMN product_id uuid;
CREATE INDEX customer_events_product_idx ON customer_events (product_id, occurred_at DESC) WHERE product_id IS NOT NULL;

-- A brand's lifecycle settings. enabled_at is when the brand turned
-- stages on: events from before it never move anyone.
CREATE TABLE lifecycle_settings (
    brand_id      uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled_at    timestamptz,
    -- Where products come from: billing connections, the API, names in
    -- product events, and by hand. A source turned off adds no held names.
    sources       text[] NOT NULL DEFAULT '{billing,api,event,manual}',
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX lifecycle_settings_workspace_idx ON lifecycle_settings (workspace_id);

-- A brand's stages, per level. rank orders them for the company's overall
-- status (the highest wins); category says what a stage means, so the
-- engine knows which stages count as active, at risk or churned.
CREATE TABLE lifecycle_stages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    level         text NOT NULL CHECK (level IN ('company', 'person')),
    -- Lowercase letters, digits and underscores (the API checks).
    key           text NOT NULL CHECK (length(key) BETWEEN 1 AND 40),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    category      text NOT NULL CHECK (category IN ('lead', 'trial', 'active', 'at_risk', 'churned')),
    rank          integer NOT NULL CHECK (rank BETWEEN 0 AND 1000),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, level, key)
);

-- What moves someone between stages. An event move happens when an event
-- (with its conditions) arrives for a product; an inactivity move when
-- the product has had no usage for its at_risk_days or churn_days. Each
-- move emits its event on the record's timeline.
CREATE TABLE lifecycle_moves (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    level         text NOT NULL CHECK (level IN ('company', 'person')),
    key           text NOT NULL CHECK (length(key) BETWEEN 1 AND 40),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind          text NOT NULL CHECK (kind IN ('event', 'usage', 'inactivity')),
    -- event: the event that makes the move. usage: any event about the
    -- product. inactivity: after says which of the product's days.
    event         text CHECK (length(event) BETWEEN 1 AND 100),
    after         text CHECK (after IN ('at_risk_days', 'churn_days')),
    -- Property tests on the event, in the shape journeys use.
    conditions    jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(conditions) = 'array'),
    -- The stages it moves from ("none": no stage yet; any_stage: from any
    -- stage) and the stage it moves to.
    from_stages   text[] NOT NULL DEFAULT '{}',
    any_stage     boolean NOT NULL DEFAULT false,
    to_stage      text NOT NULL,
    -- Only for products whose churn_mode is this (null: every product).
    churn_mode    text CHECK (churn_mode IN ('cancellation', 'inactivity')),
    -- The event the move emits, such as product.churned.
    emit_event    text NOT NULL CHECK (length(emit_event) BETWEEN 1 AND 100),
    position      integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- An event move names its event and an inactivity move its after, and
    -- only they do (the API checks).
    UNIQUE (brand_id, level, key)
);

-- Where each company or person stands with each product.
CREATE TABLE lifecycle_states (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    product_id    uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    level         text NOT NULL CHECK (level IN ('company', 'person')),
    stage         text NOT NULL,
    entered_at    timestamptz NOT NULL,
    last_used_at  timestamptz,
    -- The inactivity clock: the last usage or move made by an event.
    -- Inactivity moves leave it as it is, so churned for no usage counts
    -- from the last usage, not from when they became at risk.
    idle_since    timestamptz NOT NULL,
    -- When the next inactivity move is due, for the sweep.
    due_at        timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (record_id, product_id, level)
);
CREATE INDEX lifecycle_states_due_idx ON lifecycle_states (due_at) WHERE due_at IS NOT NULL;
CREATE INDEX lifecycle_states_brand_idx ON lifecycle_states (brand_id, product_id, level, stage);

-- Every move, once per cause: a retried job or a replayed event never
-- moves anyone twice.
CREATE TABLE lifecycle_transitions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- Null for a change of the company's overall status.
    product_id    uuid REFERENCES products (id) ON DELETE CASCADE,
    level         text NOT NULL CHECK (level IN ('company', 'person', 'overall')),
    move          text NOT NULL,
    from_stage    text,
    to_stage      text NOT NULL,
    -- The event that caused it (none for inactivity) and the one it
    -- emitted. No foreign keys: old events are archived.
    cause_event_id uuid,
    event_id      uuid,
    -- What makes it once: the cause event, or the inactivity due time.
    cause_key     text NOT NULL,
    at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (record_id, level, move, cause_key)
);
CREATE INDEX lifecycle_transitions_record_idx ON lifecycle_transitions (workspace_id, record_id, at DESC);

-- A company's overall status across its products: the best stage, plus
-- flags (partly_churned, trialling_another).
CREATE TABLE lifecycle_overall (
    record_id     uuid PRIMARY KEY REFERENCES records (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    status        text NOT NULL,
    flags         text[] NOT NULL DEFAULT '{}',
    -- Products in an active or at risk stage: a paying customer of them.
    active_products integer NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX lifecycle_overall_brand_idx ON lifecycle_overall (brand_id, status);

-- Query (docs/query.md): products, where records stand with each, their
-- moves and companies' overall status, and the product of each event.
CREATE OR REPLACE VIEW query.customer_events WITH (security_barrier) AS
SELECT e.id, e.record_id, e.account_id, e.brand_id, e.kind, e.name, e.user_id, e.anonymous_id, e.properties, e.context, e.source,
    e.occurred_at, e.received_at, e.product_id
FROM customer_events e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR e.brand_id = s.brand);

CREATE VIEW query.products WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.key, p.name, p.churn_mode, p.at_risk_days, p.churn_days, p.archived_at, p.created_at, p.updated_at
FROM products p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR p.brand_id = s.brand);

CREATE VIEW query.lifecycle_states WITH (security_barrier) AS
SELECT l.record_id, l.product_id, l.brand_id, l.level, l.stage, l.entered_at, l.last_used_at, l.idle_since, l.due_at
FROM lifecycle_states l JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR l.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR l.brand_id = s.brand);

CREATE VIEW query.lifecycle_moves WITH (security_barrier) AS
SELECT t.id, t.record_id, t.product_id, t.brand_id, t.level, t.move, t.from_stage, t.to_stage, t.cause_event_id, t.event_id, t.at
FROM lifecycle_transitions t JOIN query_scopes s ON s.pid = pg_backend_pid() AND t.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR t.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR t.brand_id = s.brand);

CREATE VIEW query.lifecycle_overall WITH (security_barrier) AS
SELECT o.record_id, o.brand_id, o.status, o.flags, o.active_products, o.updated_at
FROM lifecycle_overall o JOIN query_scopes s ON s.pid = pg_backend_pid() AND o.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR o.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR o.brand_id = s.brand);
