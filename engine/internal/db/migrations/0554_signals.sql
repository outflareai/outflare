-- Signals: something about a person or company that someone should act
-- on (churn risk, an upsell, a renewal coming up), tracked from raised to
-- closed with an owner, a due date and an outcome. A brand keeps its own
-- list of signal types; a type can name the events that raise it, so a
-- rule that emits account.churn_risk raises a "Churn risk" signal with no
-- code. People and agents raise them by hand too.
CREATE TABLE signal_types (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (length(key) BETWEEN 1 AND 40),
    label        text NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    -- The events that raise a signal of this type on the record they are
    -- about.
    events       text[] NOT NULL DEFAULT '{}',
    -- How long the owner has, in days, before it is late.
    due_days     integer NOT NULL DEFAULT 2 CHECK (due_days BETWEEN 0 AND 365),
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, key)
);
CREATE INDEX signal_types_workspace ON signal_types (workspace_id) WHERE archived_at IS NULL;

CREATE TABLE signals (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    type          text NOT NULL,
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    detail        text NOT NULL DEFAULT '' CHECK (length(detail) <= 2000),
    -- open: waiting for its owner. accepted: the owner is on it. done and
    -- dismissed are closed, with an outcome.
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'done', 'dismissed')),
    outcome       text CHECK (outcome IN ('won', 'no_change', 'not_useful', 'duplicate')),
    owner_id      uuid REFERENCES users(id) ON DELETE SET NULL,
    due_at        timestamptz,
    snoozed_until timestamptz,
    source        text NOT NULL CHECK (source IN ('event', 'journey', 'manual', 'agent', 'api')),
    event_id      uuid,
    data          jsonb NOT NULL DEFAULT '{}',
    -- When it was handed up because nobody acted in time.
    escalated_at  timestamptz,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    closed_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz
);
CREATE INDEX signals_queue ON signals (workspace_id, brand_id, status, due_at);
CREATE INDEX signals_record ON signals (record_id, created_at DESC);
CREATE INDEX signals_owner ON signals (workspace_id, owner_id) WHERE status IN ('open', 'accepted');
CREATE INDEX signals_late ON signals (due_at) WHERE status = 'open' AND escalated_at IS NULL;
