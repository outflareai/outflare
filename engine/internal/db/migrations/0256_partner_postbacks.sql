-- Postbacks out: an affiliate's own tracker, called with each conversion
-- credited to them. The URL is the partner's template, with
-- {placeholders} the job fills in. Each call is kept, so the partner and
-- the team can see what was sent and how it went.
ALTER TABLE partner_memberships ADD COLUMN postback_url text NOT NULL DEFAULT '' CHECK (length(postback_url) <= 2000);

CREATE TABLE partner_postback_sends (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    conversion_id uuid NOT NULL REFERENCES partner_conversions (id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'failed')),
    http_status   integer,
    error         text NOT NULL DEFAULT '' CHECK (length(error) <= 500),
    attempts      integer NOT NULL DEFAULT 0,
    sent_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversion_id, membership_id)
);
CREATE INDEX partner_postback_sends_membership_idx ON partner_postback_sends (membership_id, created_at DESC);
