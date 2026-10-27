-- Outside platforms in mailbox allocation: campaigns on EmailBison,
-- Smartlead, Instantly and Apollo linked to Outflare sequences, and what
-- each connected platform account reports about the mailboxes in it.
--
-- The sequence is the logical campaign: it carries the team, priority,
-- target and project. A link says which platform campaign(s) it runs as.
-- A sequence with no links sends through Outflare itself.

-- false: Outflare has no login for the mailbox; it was found in a
-- connected platform account and can only send there.
ALTER TABLE mailboxes ADD COLUMN has_login boolean NOT NULL DEFAULT true;

CREATE TABLE campaign_links (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    sequence_id     uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    external_id     text NOT NULL,
    external_name   text NOT NULL DEFAULT '',
    -- The platform's own word for the campaign's state: active, paused,
    -- completed, draft.
    external_status text NOT NULL DEFAULT '',
    -- full: Outflare attaches and detaches mailboxes. read_only: the
    -- platform cannot be told which mailboxes a campaign uses (Apollo picks
    -- one per contact), so its mailboxes are watched and counted as busy.
    control         text NOT NULL DEFAULT 'full' CHECK (control IN ('full', 'read_only')),
    -- Days from a lead's first email to its last step, for how long a
    -- mailbox leaving the campaign keeps sending follow-ups.
    sequence_days   integer,
    -- The campaign's own daily cap on the platform, when it has one.
    daily_limit     integer,
    last_synced_at  timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (integration_id, external_id)
);

CREATE INDEX campaign_links_seq_idx ON campaign_links (sequence_id);
CREATE INDEX campaign_links_ws_idx ON campaign_links (workspace_id);

-- What a connected platform account reported about a mailbox at its last
-- sync. A mailbox seen in two places is a conflict: it has one home.
CREATE TABLE mailbox_platforms (
    mailbox_id     uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The platform's id for the mailbox, used in its API calls.
    external_id    text NOT NULL,
    -- ok, paused, error or disconnected, in the platform's opinion.
    status         text NOT NULL,
    status_reason  text,
    daily_limit    integer,
    warmup_enabled boolean NOT NULL DEFAULT false,
    -- The platform's warm-up health score, 0-100, when it reports one.
    warmup_score   integer,
    -- Platform campaign ids the mailbox is attached to.
    campaign_ids   text[] NOT NULL DEFAULT '{}',
    observed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, integration_id)
);

CREATE INDEX mailbox_platforms_int_idx ON mailbox_platforms (integration_id);

-- Each platform account's last sync. A stale or failing account makes
-- every mailbox seen there count as busy: missing data never means free.
CREATE TABLE platform_syncs (
    integration_id uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    synced_at      timestamptz,
    attempted_at   timestamptz,
    error          text,
    mailboxes      integer NOT NULL DEFAULT 0,
    campaigns      integer NOT NULL DEFAULT 0,
    -- full: the connector can attach and detach mailboxes. read_only: it
    -- can only report them.
    control        text NOT NULL DEFAULT 'full' CHECK (control IN ('full', 'read_only')),
    -- An account connected with live campaigns comes in as it is, and
    -- nothing moves until a person reviews and applies the first plan.
    reviewed_at    timestamptz
);

-- An assignment through a platform campaign. confirmed_at is set when the
-- platform reads back with the mailbox attached; until then it adds no
-- capacity. drain_until is when a mailbox draining out of a platform
-- campaign has finished its follow-ups there (platforms do not report
-- this per mailbox, so it is the campaign's sequence length).
ALTER TABLE mailbox_assignments
    ADD COLUMN link_id      uuid REFERENCES campaign_links (id) ON DELETE SET NULL,
    ADD COLUMN confirmed_at timestamptz,
    ADD COLUMN drain_until  timestamptz,
    DROP CONSTRAINT mailbox_assignments_source_check,
    ADD CONSTRAINT mailbox_assignments_source_check CHECK (source IN ('manual', 'automatic', 'external'));

-- external: someone attached or detached the mailbox inside the platform,
-- and Outflare accepted it as a manual change.
ALTER TABLE allocation_changes
    ADD COLUMN link_id uuid REFERENCES campaign_links (id) ON DELETE SET NULL,
    DROP CONSTRAINT allocation_changes_source_check,
    ADD CONSTRAINT allocation_changes_source_check CHECK (source IN ('manual', 'automatic', 'approval', 'undo', 'external'));
