-- Rules: the rules engine of the customer data platform. A rule watches
-- what happens to people and other records and, when its condition
-- becomes true, emits a business event of its own (such as
-- customer.reactivated) on the record's timeline. Journeys, webhooks and
-- anything else that listens to events act on it; a rule never sends
-- anything itself.
--
-- Four kinds of trigger:
--   event     an event happens (product_activated with plan = pro)
--   change    a record's fields come to meet the conditions (lifecycle
--             stage becomes customer)
--   absence   something doesn't happen in time (no product_activated
--             within 7 days of signed_up, or no api_called for 14 days)
--   audience  someone joins or leaves a smart list
CREATE TABLE rules (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: records of every brand.
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description   text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'archived')),
    -- The record type a rule is about: person for event, absence and
    -- audience rules, any type for a change rule.
    record_type   text NOT NULL DEFAULT 'person',
    trigger_kind  text NOT NULL CHECK (trigger_kind IN ('event', 'change', 'absence', 'audience')),
    -- The trigger's settings, in the shape rules.Trigger describes.
    trigger       jsonb NOT NULL CHECK (jsonb_typeof(trigger) = 'object'),
    -- Attribute and property tests, and whether all or any must hold.
    conditions    jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(conditions) = 'array'),
    match         text NOT NULL DEFAULT 'all' CHECK (match IN ('all', 'any')),
    -- The event the rule emits; the API checks it is an event name.
    emit_event    text NOT NULL CHECK (length(emit_event) BETWEEN 1 AND 100),
    -- every_time: each time the condition becomes true (a new episode).
    -- once: once per record, ever.
    fire          text NOT NULL DEFAULT 'every_time' CHECK (fire IN ('every_time', 'once')),
    -- At least this long between two firings for one record.
    cooldown_seconds integer NOT NULL DEFAULT 0 CHECK (cooldown_seconds BETWEEN 0 AND 31536000),
    -- Whether a change someone made by hand can fire a change rule. Off by
    -- default: a correction shouldn't set off automations.
    manual_edits  boolean NOT NULL DEFAULT false,
    -- What the hot paths look up, kept from trigger: the events an event
    -- or absence rule listens to, the fields a change rule tests, and an
    -- audience rule's smart list.
    watch_events  text[] NOT NULL DEFAULT '{}',
    watch_fields  text[] NOT NULL DEFAULT '{}',
    list_id       uuid REFERENCES smart_lists (id) ON DELETE SET NULL,
    version       integer NOT NULL DEFAULT 1,
    activated_at  timestamptz,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX rules_name_idx ON rules (workspace_id, lower(name)) WHERE status <> 'archived';
CREATE INDEX rules_active_idx ON rules (workspace_id, trigger_kind) WHERE status = 'active';
CREATE INDEX rules_list_idx ON rules (list_id) WHERE status = 'active' AND list_id IS NOT NULL;

-- Where each record stands with each rule: whether the condition held at
-- the last check (so a rule fires when it becomes true, not while it stays
-- true), how many episodes it has had, and an absence rule's timer.
CREATE TABLE rule_states (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    rule_id       uuid NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    holds         boolean NOT NULL DEFAULT false,
    episode       integer NOT NULL DEFAULT 0,
    -- Absence: when the time runs out, and the event that started it.
    due_at        timestamptz,
    anchor_at     timestamptz,
    anchor_event_id uuid,
    anchor_properties jsonb NOT NULL DEFAULT '{}',
    last_fired_at timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, record_id)
);
CREATE INDEX rule_states_due_idx ON rule_states (due_at) WHERE due_at IS NOT NULL;
CREATE INDEX rule_states_record_idx ON rule_states (workspace_id, record_id);

-- Each time a rule fired: once per rule, record and episode, so a retry,
-- a replay or a duplicate event never fires twice.
CREATE TABLE rule_firings (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    rule_id       uuid NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
    rule_version  integer NOT NULL,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    episode_key   text NOT NULL,
    -- The event the rule emitted, and the one that set it off (none for an
    -- absence rule's timer). No foreign keys: old events are archived.
    event_id      uuid,
    cause_event_id uuid,
    -- 1 for a rule set off by anything but another rule; a rule set off by
    -- a rule's event is one deeper. Rules stop at rules.MaxDepth.
    depth         integer NOT NULL DEFAULT 1,
    fired_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (rule_id, record_id, episode_key)
);
CREATE INDEX rule_firings_rule_idx ON rule_firings (rule_id, fired_at DESC, id DESC);
CREATE INDEX rule_firings_event_idx ON rule_firings (event_id) WHERE event_id IS NOT NULL;
CREATE INDEX rule_firings_record_idx ON rule_firings (workspace_id, record_id);
