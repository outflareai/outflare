-- Inbound email (engine/internal/inboundemail): addresses a workspace
-- forwards mail to, one per purpose (AI receipts, support). The server's
-- operator turns it on through an extension, which delivers each message
-- to the engine; it is off otherwise. The engine keeps every address,
-- message and result.

-- A workspace's address for one purpose, local_part@<inbound domain>.
-- Purposes that face customers (support) have one per brand and a name
-- people can read, such as northpeak-support; the others are one per
-- workspace with letters nobody can guess. Replacing or renaming an
-- address disables the old one, which then refuses mail.
CREATE TABLE inbound_addresses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    purpose text NOT NULL CHECK (purpose IN ('ai_receipts', 'support')),
    brand_id uuid REFERENCES brands (id) ON DELETE CASCADE,
    local_part text NOT NULL UNIQUE CHECK (local_part ~ '^[a-z0-9][a-z0-9_.-]{2,62}[a-z0-9]$'),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz,
    -- Gmail's confirmation for forwarding a mailbox to this address, for
    -- purposes whose mail any address may forward (support): the address
    -- that asked, and the code and link an admin confirms it with.
    confirmation_from text CHECK (length(confirmation_from) <= 254),
    confirmation_code text CHECK (length(confirmation_code) <= 64),
    confirmation_url text CHECK (length(confirmation_url) <= 2048),
    confirmation_at timestamptz,
    last_email_at timestamptz
);
CREATE UNIQUE INDEX inbound_addresses_one_live_idx ON inbound_addresses (workspace_id, purpose, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE disabled_at IS NULL;

-- Addresses allowed to forward mail to the workspace, besides its
-- members' own, for purposes only members forward (AI receipts): a
-- member's personal address they linked. Gmail's confirmation for
-- forwarding from it is kept here for them to click.
CREATE TABLE inbound_forwarders (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email text NOT NULL CHECK (email = lower(email) AND length(email) <= 254),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    confirmation_code text CHECK (length(confirmation_code) <= 64),
    confirmation_url text CHECK (length(confirmation_url) <= 2048),
    confirmation_at timestamptz,
    last_email_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, email)
);

-- Every message delivered to an address, and what became of it. The raw
-- message is kept 30 days to process again, then deleted; the rest stays.
CREATE TABLE inbound_emails (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    address_id uuid NOT NULL REFERENCES inbound_addresses (id) ON DELETE CASCADE,
    purpose text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    message_id text CHECK (length(message_id) <= 998),
    from_address text CHECK (length(from_address) <= 254),
    forwarded_by text CHECK (length(forwarded_by) <= 254),
    subject text CHECK (length(subject) <= 998),
    -- Domains whose DKIM signature on the message checked out.
    dkim_domains text[] NOT NULL DEFAULT '{}',
    -- received, then processed, ignored (not for this purpose), rejected
    -- (refused, see reason) or failed (Outflare's error, retried).
    status text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'processed', 'ignored', 'rejected', 'failed')),
    reason text CHECK (length(reason) <= 64),
    raw bytea,
    size_bytes integer NOT NULL DEFAULT 0,
    processed_at timestamptz
);
CREATE INDEX inbound_emails_workspace_idx ON inbound_emails (workspace_id, received_at DESC);
CREATE INDEX inbound_emails_raw_idx ON inbound_emails (received_at) WHERE raw IS NOT NULL;
CREATE UNIQUE INDEX inbound_emails_message_idx ON inbound_emails (address_id, message_id) WHERE message_id IS NOT NULL;
