-- One inbox for every system: replies from connected sequencers land beside
-- the ones Outflare's own mailboxes receive, with more labels, tags people
-- manage, saved views, and webhooks that can listen for some labels only.

-- Three more labels: objection (a reason not to buy, worth an answer),
-- complaint (angry, a spam report or a legal threat) and auto_reply
-- (automatic mail that isn't an out-of-office).
ALTER TABLE inbound_messages DROP CONSTRAINT IF EXISTS inbound_messages_category_check;
ALTER TABLE inbound_messages ADD CONSTRAINT inbound_messages_category_check CHECK (category IN (
    'interested', 'meeting_booked', 'question', 'referral', 'not_now', 'objection',
    'not_interested', 'wrong_person', 'unsubscribe', 'complaint', 'out_of_office', 'auto_reply'));

-- Who set the label: the built-in rules, the AI, a person, an API token or
-- an agent.
ALTER TABLE inbound_messages DROP CONSTRAINT IF EXISTS inbound_messages_category_source_check;
ALTER TABLE inbound_messages ADD CONSTRAINT inbound_messages_category_source_check CHECK (category_source IN (
    'rules', 'ai', 'human', 'api_token', 'agent', 'system'));

ALTER TABLE inbound_messages
    -- Where the reply came from: 'mailbox' for Outflare's own mailboxes,
    -- else the connector id of the sequencer it was read from.
    ADD COLUMN source text NOT NULL DEFAULT 'mailbox',
    ADD COLUMN integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The reply's id at the sequencer, to read it once and answer through it.
    ADD COLUMN external_id text,
    -- How sure whoever labelled it was, 0 to 1. Rules and people are 1.
    ADD COLUMN category_confidence real CHECK (category_confidence BETWEEN 0 AND 1),
    -- Why it was labelled so, in a few words (the objection reason, say),
    -- and details read from the reply: a return date, a referred contact.
    ADD COLUMN category_reason text,
    ADD COLUMN details jsonb NOT NULL DEFAULT '{}',
    -- A person should look: the label is unsure, or an unsure opt-out was
    -- acted on and may need undoing.
    ADD COLUMN needs_review boolean NOT NULL DEFAULT false,
    ADD COLUMN classified_at timestamptz;

CREATE UNIQUE INDEX inbound_external_idx ON inbound_messages (integration_id, external_id)
    WHERE external_id IS NOT NULL;
CREATE INDEX inbound_review_idx ON inbound_messages (workspace_id, received_at DESC) WHERE needs_review;

-- Replies read from a sequencer have no IMAP folder; they number their
-- uid from this sequence with uidvalidity 0.
CREATE SEQUENCE inbound_external_uid_seq;

-- Tags people manage. A reply's labels column holds tag names; this table
-- gives them colours and a list to pick from.
CREATE TABLE inbox_tags (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL, -- lowercase, as stored on replies
    color        text NOT NULL DEFAULT 'gray',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

-- Saved filters for the inbox. user_id NULL: shared with the workspace.
CREATE TABLE inbox_views (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid REFERENCES users (id) ON DELETE CASCADE,
    name         text NOT NULL,
    filters      jsonb NOT NULL DEFAULT '{}',
    position     integer NOT NULL DEFAULT 0,
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inbox_views_ws_idx ON inbox_views (workspace_id, position);

-- A webhook can listen for replies with some labels only (say, the hot
-- ones). Empty: every label. Events without a label are not filtered.
ALTER TABLE webhook_endpoints ADD COLUMN categories text[] NOT NULL DEFAULT '{}';
