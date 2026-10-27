-- Tracking domains and click tracking. Both are optional: by default
-- unsubscribe links use the instance's own address and links in emails are
-- left exactly as written.

-- A workspace's own domain (e.g. links.acme.com, a CNAME to this instance)
-- for unsubscribe and click links, so they don't share a domain's
-- reputation with every other sender on the instance.
CREATE TABLE tracking_domains (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    domain          text NOT NULL UNIQUE CHECK (domain = lower(domain)),
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'failed')),
    error           text NOT NULL DEFAULT '',
    last_checked_at timestamptz,
    verified_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tracking_domains_ws_idx ON tracking_domains (workspace_id);

ALTER TABLE sequences
    -- Rewrite links so clicks are counted. Off by default: plain links
    -- read as more personal and some filters distrust redirects.
    ADD COLUMN track_clicks boolean NOT NULL DEFAULT false,
    -- Add the List-Unsubscribe header (one-click unsubscribe in Gmail and
    -- Yahoo). On by default; required for bulk senders.
    ADD COLUMN unsubscribe_header boolean NOT NULL DEFAULT true;

-- One row per distinct link in a step sent to one lead.
CREATE TABLE tracked_links (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enrollment_id  uuid NOT NULL REFERENCES enrollments (id) ON DELETE CASCADE,
    sequence_id    uuid REFERENCES sequences (id) ON DELETE SET NULL,
    step_position  integer NOT NULL,
    url            text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (enrollment_id, step_position, url)
);

CREATE TABLE link_clicks (
    id         bigserial PRIMARY KEY,
    link_id    uuid NOT NULL REFERENCES tracked_links (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Security scanners open links before people do; such clicks are kept
    -- but not counted.
    likely_bot boolean NOT NULL DEFAULT false,
    user_agent text NOT NULL DEFAULT '',
    clicked_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX link_clicks_ws_idx ON link_clicks (workspace_id, clicked_at);
CREATE INDEX link_clicks_link_idx ON link_clicks (link_id);
