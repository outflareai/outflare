-- Support history from another tool: conversations a brand answers in
-- Intercom (and its AI agent, Fin) are copied into the support inbox as
-- they are, so Outflare has the same data as if the team worked here. An
-- imported conversation is read-only in Outflare: it is answered in the
-- tool it came from, and each sync brings the changes over.

-- Teammates, teams and bots of the other tool, as it names them, so an
-- imported message says who wrote it. user_id is the Outflare user with the
-- same email address, when there is one; the address itself isn't kept.
CREATE TABLE support_external_agents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The tool (intercom) and its account (Intercom's app id), so ids from
    -- two accounts never clash.
    source       text NOT NULL CHECK (source = ANY (ARRAY['intercom'])),
    external_app text NOT NULL CHECK (length(external_app) BETWEEN 1 AND 100),
    -- admin: a teammate. team: a team. bot: an automation or AI agent.
    kind         text NOT NULL CHECK (kind IN ('admin', 'team', 'bot')),
    external_id  text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 100),
    name         text NOT NULL DEFAULT '' CHECK (length(name) <= 300),
    user_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX support_external_agents_key ON support_external_agents (workspace_id, source, external_app, kind, external_id);
CREATE INDEX support_external_agents_brand ON support_external_agents (brand_id);

ALTER TABLE support_conversations
    -- outflare: it started here. intercom: copied from Intercom.
    ADD COLUMN source               text NOT NULL DEFAULT 'outflare' CHECK (source IN ('outflare', 'intercom')),
    ADD COLUMN integration_id       uuid REFERENCES integrations (id) ON DELETE SET NULL,
    ADD COLUMN external_app         text CHECK (length(external_app) <= 100),
    ADD COLUMN external_id          text CHECK (length(external_id) <= 100),
    -- The other tool's own name for where it came in (Intercom's
    -- source.type: conversation, email, whatsapp, phone_call ...); channel
    -- stays email or chat.
    ADD COLUMN external_channel     text CHECK (length(external_channel) <= 60),
    ADD COLUMN external_assignee_id uuid REFERENCES support_external_agents (id) ON DELETE SET NULL,
    ADD COLUMN external_team_id     uuid REFERENCES support_external_agents (id) ON DELETE SET NULL,
    ADD COLUMN priority             text NOT NULL DEFAULT '' CHECK (priority IN ('', 'low', 'medium', 'high', 'urgent')),
    ADD COLUMN reopens              integer NOT NULL DEFAULT 0 CHECK (reopens >= 0),
    -- The customer's rating of the conversation (1 to 5) and what they wrote.
    ADD COLUMN rating               smallint CHECK (rating BETWEEN 1 AND 5),
    ADD COLUMN rating_remark        text CHECK (length(rating_remark) <= 4000),
    ADD COLUMN rated_at             timestamptz,
    -- What another tool's AI agent did in it (Intercom's Fin): which agent,
    -- how it ended (Intercom's resolution_state, such as
    -- assumed_resolution, confirmed_resolution or escalated), the kind of
    -- its last answer, the workflow or setup that ran it, the customer's
    -- rating of it, and the sources it answered from ([{type, title, url,
    -- locale}]). Empty when no AI agent took part.
    ADD COLUMN ai_agent             text NOT NULL DEFAULT '' CHECK (ai_agent IN ('', 'fin')),
    ADD COLUMN ai_resolution        text NOT NULL DEFAULT '' CHECK (length(ai_resolution) <= 60),
    ADD COLUMN ai_answer_type       text NOT NULL DEFAULT '' CHECK (length(ai_answer_type) <= 60),
    ADD COLUMN ai_source_type       text NOT NULL DEFAULT '' CHECK (length(ai_source_type) <= 60),
    ADD COLUMN ai_source_title      text NOT NULL DEFAULT '' CHECK (length(ai_source_title) <= 500),
    ADD COLUMN ai_rating            smallint CHECK (ai_rating BETWEEN 1 AND 5),
    ADD COLUMN ai_rating_remark     text CHECK (length(ai_rating_remark) <= 4000),
    ADD COLUMN ai_sources           jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(ai_sources) = 'array'),
    -- When the other tool last changed it, so a sync skips what it has.
    ADD COLUMN external_updated_at  timestamptz;
CREATE UNIQUE INDEX support_conversations_external ON support_conversations (workspace_id, source, external_app, external_id)
    WHERE external_id IS NOT NULL;
CREATE INDEX support_conversations_ai ON support_conversations (workspace_id, ai_agent, ai_resolution) WHERE ai_agent <> '';

ALTER TABLE support_messages
    ADD COLUMN external_id        text CHECK (length(external_id) <= 100),
    -- Who wrote it: the customer, a teammate, an AI agent, or the tool
    -- itself. Empty on messages from before this column.
    ADD COLUMN author_type        text NOT NULL DEFAULT '' CHECK (author_type IN ('', 'customer', 'agent', 'ai', 'system')),
    ADD COLUMN external_author_id uuid REFERENCES support_external_agents (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX support_messages_external ON support_messages (conversation_id, external_id) WHERE external_id IS NOT NULL;

-- One sync per connection: what it has copied so far and where it is.
-- A sync pages through the conversations the tool changed since
-- synced_through, keeping its place in cursor between jobs, then moves
-- synced_through to when that pass started.
CREATE TABLE support_imports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    integration_id   uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    source           text NOT NULL CHECK (source = ANY (ARRAY['intercom'])),
    external_app     text NOT NULL DEFAULT '' CHECK (length(external_app) <= 100),
    -- syncing: a pass is running. idle: caught up, the next pass runs on
    -- schedule. paused: a person stopped it. failed: the last pass failed
    -- (error says why) and is retried on schedule.
    status           text NOT NULL DEFAULT 'syncing' CHECK (status IN ('syncing', 'idle', 'paused', 'failed')),
    -- History to copy: conversations changed since this time, or all of
    -- them when null.
    history_from     timestamptz,
    synced_through   timestamptz,
    pass_started_at  timestamptz,
    cursor           text CHECK (length(cursor) <= 2000),
    conversations    integer NOT NULL DEFAULT 0 CHECK (conversations >= 0),
    messages         integer NOT NULL DEFAULT 0 CHECK (messages >= 0),
    last_synced_at   timestamptz,
    error            text CHECK (length(error) <= 1000),
    error_code       text CHECK (length(error_code) <= 60),
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX support_imports_integration ON support_imports (integration_id);
CREATE INDEX support_imports_brand ON support_imports (workspace_id, brand_id);
CREATE INDEX support_imports_due ON support_imports (status, last_synced_at);
