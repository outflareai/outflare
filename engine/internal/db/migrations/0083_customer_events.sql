-- Product events and consents: what people do in the workspace's own
-- product, and what they agreed to be sent. Both hang off customer records
-- (0082), so segments, rules, journeys, the CRM and lifecycle email read the
-- same facts.

-- One row per event the workspace sent: track, identify, page, screen,
-- group or alias, in the shape Segment uses. record_id is the person it was
-- matched to, or null for an anonymous visitor until they are identified.
CREATE TABLE customer_events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid REFERENCES records (id) ON DELETE SET NULL,
    -- The company a group call named.
    account_id   uuid REFERENCES records (id) ON DELETE SET NULL,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    kind         text NOT NULL CHECK (kind IN ('track', 'identify', 'page', 'screen', 'group', 'alias')),
    -- The catalog name (Order Completed becomes order_completed; a page is
    -- page.viewed), and the name as it was sent.
    name         text NOT NULL CHECK (name ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$' AND length(name) <= 100),
    raw_name     text NOT NULL DEFAULT '',
    user_id      text,
    anonymous_id text,
    properties   jsonb NOT NULL DEFAULT '{}',
    traits       jsonb NOT NULL DEFAULT '{}',
    context      jsonb NOT NULL DEFAULT '{}',
    source       text NOT NULL DEFAULT 'api',
    -- The sender's id for the message, so a retry is stored once.
    message_id   text,
    occurred_at  timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    request_id   text NOT NULL DEFAULT '',
    UNIQUE (workspace_id, message_id)
);

CREATE INDEX customer_events_record_idx ON customer_events (record_id, occurred_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX customer_events_name_idx ON customer_events (workspace_id, name, occurred_at DESC);
CREATE INDEX customer_events_recent_idx ON customer_events (workspace_id, occurred_at DESC, id DESC);
CREATE INDEX customer_events_anonymous_idx ON customer_events (workspace_id, anonymous_id) WHERE record_id IS NULL AND anonymous_id IS NOT NULL;

-- Consent is a log: every grant and withdrawal is kept with its evidence,
-- and the latest row for a record, channel, topic and purpose is the
-- current state. Lifecycle email sends marketing only on a current grant.
CREATE TABLE consents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('email', 'sms', 'push', 'whatsapp', 'in_app')),
    -- A subscription topic's key, such as product_updates, or '' for every
    -- topic on the channel.
    topic        text NOT NULL DEFAULT '' CHECK (topic = '' OR topic ~ '^[a-z][a-z0-9_]{1,62}$'),
    purpose      text NOT NULL DEFAULT 'marketing' CHECK (purpose ~ '^[a-z][a-z0-9_]{1,39}$'),
    status       text NOT NULL CHECK (status IN ('granted', 'withdrawn')),
    -- Where it was given or taken back: signup_form, preference_center,
    -- unsubscribe_link, import, api.
    source       text NOT NULL DEFAULT 'api',
    -- What proves it: the form, the wording's version, the address.
    evidence     jsonb NOT NULL DEFAULT '{}',
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    request_id   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX consents_current_idx ON consents (record_id, channel, topic, purpose, occurred_at DESC, created_at DESC);
CREATE INDEX consents_workspace_idx ON consents (workspace_id, occurred_at DESC);
