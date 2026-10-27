-- Outcomes and cost: meetings booked from outreach, deals in the CRM, and a
-- ledger of what the workspace spent to get them. The ledger records cost;
-- it never bills. Customers pay their providers directly.
--
-- mission_id has no foreign key here: missions arrive in a later migration,
-- which adds the constraint.

-- Money and credits spent. One row per charge: a batch of verification
-- credits, a domain renewal, an enrichment run. Rows with a ref are
-- idempotent: recording the same ref twice keeps the first row.
CREATE TABLE spend_entries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id     uuid REFERENCES projects (id) ON DELETE SET NULL,
    sequence_id    uuid REFERENCES sequences (id) ON DELETE SET NULL,
    mission_id     uuid,
    -- Infrastructure the charge was for, so reports can follow it to the
    -- project and to the campaigns that sent from it.
    mailbox_id     uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    domain         text NOT NULL DEFAULT '' CHECK (domain = lower(domain)), -- a sending domain
    category       text NOT NULL CHECK (category IN
                   ('data', 'verification', 'mailboxes', 'domains', 'sending', 'software', 'people', 'ads', 'other')),
    -- Connector id or a free-text vendor name, e.g. 'zerobounce', 'Namecheap'.
    provider       text NOT NULL DEFAULT '',
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- What was consumed, when known: 1200 credits, 3 domains.
    units          numeric(18, 4),
    unit_name      text NOT NULL DEFAULT '',
    -- The money, when known. Null: only units are known; reports price them
    -- with spend_rates and mark the result estimated.
    amount_cents   bigint CHECK (amount_cents >= 0),
    currency       text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    source         text NOT NULL DEFAULT 'manual' CHECK (source IN ('auto', 'manual', 'import')),
    ref            text,
    note           text NOT NULL DEFAULT '',
    -- Who caused it: a person, an API token or an agent.
    actor_id       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (units IS NOT NULL OR amount_cents IS NOT NULL)
);

CREATE INDEX spend_entries_ws_idx ON spend_entries (workspace_id, occurred_at DESC);
CREATE INDEX spend_entries_seq_idx ON spend_entries (sequence_id, occurred_at);
CREATE INDEX spend_entries_project_idx ON spend_entries (project_id, occurred_at);
CREATE INDEX spend_entries_mission_idx ON spend_entries (mission_id) WHERE mission_id IS NOT NULL;
CREATE UNIQUE INDEX spend_entries_ref_idx ON spend_entries (workspace_id, ref) WHERE ref IS NOT NULL;

-- Costs that repeat: mailbox seats, a data subscription, an SDR's time.
-- Reports spread them evenly over each day they were active.
CREATE TABLE spend_recurring (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id   uuid REFERENCES projects (id) ON DELETE SET NULL,
    sequence_id  uuid REFERENCES sequences (id) ON DELETE SET NULL,
    category     text NOT NULL CHECK (category IN
                 ('data', 'verification', 'mailboxes', 'domains', 'sending', 'software', 'people', 'ads', 'other')),
    provider     text NOT NULL DEFAULT '',
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    amount_cents bigint NOT NULL CHECK (amount_cents >= 0),
    currency     text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    every        text NOT NULL DEFAULT 'month' CHECK (every IN ('month', 'year')),
    starts_on    date NOT NULL DEFAULT current_date,
    ends_on      date,
    note         text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_on IS NULL OR ends_on >= starts_on)
);

CREATE INDEX spend_recurring_ws_idx ON spend_recurring (workspace_id);

-- What a unit costs, for entries that only know units.
CREATE TABLE spend_rates (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    provider       text NOT NULL,
    unit_name      text NOT NULL,
    -- Millionths of the currency unit, so a $0.004 credit is 4000.
    unit_price_micros bigint NOT NULL CHECK (unit_price_micros >= 0),
    currency       text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, provider, unit_name)
);

-- Meetings with leads. Synced from calendars, booking tools and the CRM, or
-- logged by a person or an agent. Only events with a lead among the
-- attendees are stored; the rest of a calendar is never copied.
CREATE TABLE meetings (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id       uuid REFERENCES projects (id) ON DELETE SET NULL,
    mission_id       uuid,
    sequence_id      uuid REFERENCES sequences (id) ON DELETE SET NULL,
    lead_id          uuid REFERENCES leads (id) ON DELETE SET NULL,
    -- The reply that led to the meeting, when one is known.
    inbound_id       uuid REFERENCES inbound_messages (id) ON DELETE SET NULL,
    source           text NOT NULL CHECK (source IN
                     ('google_calendar', 'microsoft_calendar', 'calendly', 'cal_com', 'hubspot', 'manual', 'api')),
    integration_id   uuid REFERENCES integrations (id) ON DELETE SET NULL,
    external_id      text,
    title            text NOT NULL DEFAULT '',
    starts_at        timestamptz NOT NULL,
    ends_at          timestamptz,
    booked_at        timestamptz NOT NULL DEFAULT now(),
    status           text NOT NULL DEFAULT 'booked' CHECK (status IN ('booked', 'held', 'no_show', 'canceled')),
    attendee_email   text NOT NULL DEFAULT '',
    attendee_name    text NOT NULL DEFAULT '',
    organizer_email  text NOT NULL DEFAULT '',
    -- How the meeting was tied to outreach. lead: an attendee is a lead.
    -- company: an attendee shares a lead's company domain. manual: someone
    -- said so. none: not tied to any outreach (still counted for the
    -- workspace, never for a campaign).
    attribution      text NOT NULL DEFAULT 'none' CHECK (attribution IN ('lead', 'company', 'manual', 'none')),
    -- Set by a person: a pinned attribution is never changed by sync.
    attribution_pinned boolean NOT NULL DEFAULT false,
    -- Whether the meeting counts as qualified. Null: not judged yet.
    qualified        boolean,
    notes            text NOT NULL DEFAULT '',
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX meetings_external_idx ON meetings (workspace_id, source, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX meetings_ws_idx ON meetings (workspace_id, starts_at DESC);
CREATE INDEX meetings_seq_idx ON meetings (sequence_id, booked_at);
CREATE INDEX meetings_lead_idx ON meetings (lead_id);

-- Deals, read from the CRM (or logged by hand when there is none). The CRM
-- stays the source of truth; these rows are a copy for reporting.
CREATE TABLE deals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id     uuid REFERENCES projects (id) ON DELETE SET NULL,
    mission_id     uuid,
    sequence_id    uuid REFERENCES sequences (id) ON DELETE SET NULL,
    lead_id        uuid REFERENCES leads (id) ON DELETE SET NULL,
    meeting_id     uuid REFERENCES meetings (id) ON DELETE SET NULL,
    source         text NOT NULL CHECK (source IN ('hubspot', 'manual', 'api')),
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    external_id    text,
    name           text NOT NULL DEFAULT '',
    stage          text NOT NULL DEFAULT '',
    state          text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'won', 'lost')),
    amount_cents   bigint CHECK (amount_cents >= 0),
    currency       text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    opened_at      timestamptz NOT NULL DEFAULT now(),
    closed_at      timestamptz,
    attribution    text NOT NULL DEFAULT 'none' CHECK (attribution IN ('lead', 'company', 'manual', 'none')),
    attribution_pinned boolean NOT NULL DEFAULT false,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX deals_external_idx ON deals (workspace_id, source, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX deals_ws_idx ON deals (workspace_id, opened_at DESC);
CREATE INDEX deals_seq_idx ON deals (sequence_id);

-- Where each outcome sync left off.
CREATE TABLE outcome_syncs (
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    stream         text NOT NULL, -- 'meetings', 'deals', 'push'
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cursor         text NOT NULL DEFAULT '',
    last_run_at    timestamptz,
    last_ok_at     timestamptz,
    last_error     text,
    -- Rows created or changed by the last run.
    last_changes   integer NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, stream)
);

-- Outcomes written into another system, so each is written once.
CREATE TABLE outcome_links (
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('meeting', 'contact')),
    local_id       uuid NOT NULL,
    external_id    text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, kind, local_id)
);
