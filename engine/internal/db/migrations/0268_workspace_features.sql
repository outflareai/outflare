-- Parts of Outflare that stay off until the operator switches them on for
-- a workspace (engine/internal/features), such as AI Accounts while
-- it is tested. A row means the feature is on for everyone in the
-- workspace; turning it off deletes the row.
CREATE TABLE workspace_features (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    feature text NOT NULL CHECK (feature ~ '^[a-z][a-z0-9_]{1,62}$'),
    enabled_at timestamptz NOT NULL DEFAULT now(),
    -- Who switched it on through an extension, or null for the
    -- operator's command line.
    enabled_by uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (workspace_id, feature)
);
