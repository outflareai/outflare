-- Online and Away in the Inbox (docs/support-inbox.md, "Online and Away"):
-- whether a teammate is taking new chats for a brand right now. Someone is
-- online while their status is online and their Inbox checked in lately
-- (seen_at), so closing the page takes them out without their saying so.
-- A brand where nobody has a row works as before: chats wait unassigned.
CREATE TABLE inbox_presence (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status       text NOT NULL DEFAULT 'away' CHECK (status IN ('online', 'away')),
    seen_at      timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, user_id)
);
CREATE INDEX inbox_presence_workspace ON inbox_presence (workspace_id);
CREATE INDEX inbox_presence_user ON inbox_presence (user_id);
