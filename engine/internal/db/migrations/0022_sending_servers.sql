-- Sending servers: mailboxes that leave through the same SMTP host, and so
-- share its IP addresses and their reputation. Cold-email SMTP vendors
-- (Mailreef, Infraforge) and self-hosted mail servers look like this.
-- Google and Microsoft mailboxes have no server row: their IPs are shared
-- by the whole provider and are not the workspace's to watch or cap.

CREATE TABLE sending_servers (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The SMTP host its mailboxes sign in to, lowercased. Mailboxes are
    -- grouped by it.
    host             text NOT NULL,
    name             text NOT NULL DEFAULT '',
    -- The IPv4 and IPv6 addresses mail leaves from. dns: the host's A and
    -- AAAA records, refreshed on every check. manual: set by hand, for
    -- servers that sign in on one address and send from others.
    ips              text[] NOT NULL DEFAULT '{}',
    ips_source       text NOT NULL DEFAULT 'dns' CHECK (ips_source IN ('dns', 'manual')),
    -- paused: no sequence or test sends from any of its mailboxes.
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    -- Who paused it: a person, or the blocklist check. Only a blocklist
    -- pause lifts itself when the listing clears.
    paused_by        text CHECK (paused_by IN ('person', 'blocklist')),
    status_reason    text,
    pause_on_listing boolean NOT NULL DEFAULT true,
    -- Most sends from all of its mailboxes together in 24 hours. Null
    -- leaves only the mailbox and domain limits.
    daily_limit      integer CHECK (daily_limit BETWEEN 1 AND 1000000),
    notes            text NOT NULL DEFAULT '',
    -- [{ip, name, zone, status: clear|listed|unknown, detail}]
    blocklists       jsonb NOT NULL DEFAULT '[]',
    checked_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX sending_servers_host_idx ON sending_servers (workspace_id, host);

ALTER TABLE mailboxes ADD COLUMN server_id uuid REFERENCES sending_servers (id) ON DELETE SET NULL;
CREATE INDEX mailboxes_server_idx ON mailboxes (server_id);

-- Servers for the mailboxes already connected on other hosts.
INSERT INTO sending_servers (workspace_id, host)
SELECT DISTINCT workspace_id, lower(smtp_host) FROM mailboxes WHERE provider = 'other'
ON CONFLICT (workspace_id, host) DO NOTHING;

UPDATE mailboxes m SET server_id = s.id
FROM sending_servers s
WHERE m.provider = 'other' AND s.workspace_id = m.workspace_id AND s.host = lower(m.smtp_host);

-- Check them now, which also starts the daily re-checks.
INSERT INTO jobs (workspace_id, kind, payload, max_attempts)
SELECT workspace_id, 'server.check', jsonb_build_object('server_id', id), 3 FROM sending_servers;
