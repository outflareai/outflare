-- Broadcasts: one lifecycle email sent to many people at once, such as a
-- newsletter or a launch. The audience is people on customer records: a
-- topic's subscribers, or a list of records. In time a saved list or
-- segment (audience_ref) fills it too.
CREATE TABLE email_broadcasts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- A lifecycle template; its published version is frozen when sending starts.
    template_id      uuid NOT NULL REFERENCES email_templates (id) ON DELETE RESTRICT,
    template_version integer,
    -- The topic it is for: the template's unless set.
    topic_id         uuid REFERENCES email_topics (id) ON DELETE SET NULL,
    provider_id      uuid REFERENCES email_providers (id) ON DELETE SET NULL,
    from_address     text NOT NULL DEFAULT '',
    -- topic: everyone the topic may go to. records: the listed records.
    -- list: a saved list or segment, named by audience_ref (not yet).
    audience_kind    text NOT NULL DEFAULT 'topic' CHECK (audience_kind IN ('topic', 'records', 'list')),
    audience_ref     text,
    record_ids       uuid[] NOT NULL DEFAULT '{}',
    data             jsonb NOT NULL DEFAULT '{}',
    track_opens      boolean,
    track_clicks     boolean,
    status           text NOT NULL DEFAULT 'draft'
                         CHECK (status IN ('draft', 'scheduled', 'sending', 'sent', 'cancelled', 'failed')),
    status_reason    text,
    send_at          timestamptz,
    started_at       timestamptz,
    finished_at      timestamptz,
    -- Where the fan-out got to: the last record id it went through.
    cursor_record    uuid,
    recipients       integer NOT NULL DEFAULT 0,
    skipped          integer NOT NULL DEFAULT 0,
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    sent_by          uuid REFERENCES actors (id) ON DELETE SET NULL,
    request_id       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_broadcasts_ws ON email_broadcasts (workspace_id, created_at DESC);

-- One row per person a broadcast reached for: the email made for them, or
-- why there was none. Sending twice never emails anyone twice.
CREATE TABLE email_broadcast_recipients (
    broadcast_id uuid NOT NULL REFERENCES email_broadcasts (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL,
    address      text,
    email_id     uuid REFERENCES emails (id) ON DELETE SET NULL,
    skipped      text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (broadcast_id, record_id)
);

ALTER TABLE emails ADD COLUMN broadcast_id uuid REFERENCES email_broadcasts (id) ON DELETE SET NULL;
CREATE INDEX emails_broadcast ON emails (broadcast_id) WHERE broadcast_id IS NOT NULL;
