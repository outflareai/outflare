-- Saved replies (docs/support-inbox.md, "Saved replies"): answers a brand's
-- team writes once and drops into a reply, in support and in replies to
-- outreach. The text may hold {first_name} and {my_name}, filled in when
-- it is used.
CREATE TABLE saved_replies (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    body         text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    -- Who wrote it; null for an API token or agent, or once they leave.
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    -- How often it was dropped into a reply, so the list can lead with
    -- the ones the team uses.
    uses         integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX saved_replies_name ON saved_replies (brand_id, lower(name));
CREATE INDEX saved_replies_workspace ON saved_replies (workspace_id);
