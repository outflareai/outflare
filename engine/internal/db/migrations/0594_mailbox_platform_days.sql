-- What each mailbox did per day in a connected sequencer (EmailBison,
-- Instantly, Smartlead): campaign emails sent, replies and bounces, as the
-- sequencer counts them, read on each sync. Outflare's own sends are in
-- messages; this is the mail it doesn't send itself, so a mailbox's health
-- and activity cover both. Warm-up mail is never counted.
CREATE TABLE mailbox_platform_days (
    mailbox_id     uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The mailbox's brand, written with each day's counts.
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The day, in UTC.
    day            date NOT NULL,
    sent           integer NOT NULL DEFAULT 0,
    replies        integer NOT NULL DEFAULT 0,
    bounced        integer NOT NULL DEFAULT 0,
    observed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, integration_id, day)
);

CREATE INDEX mailbox_platform_days_ws_idx ON mailbox_platform_days (workspace_id, day);
CREATE INDEX mailbox_platform_days_brand_idx ON mailbox_platform_days (brand_id);
