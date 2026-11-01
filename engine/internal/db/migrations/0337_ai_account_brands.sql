-- A subscription is discovered at workspace level, then assigned explicitly.
-- Existing and newly discovered accounts remain unassigned; never infer a
-- brand from a browser's current selection or an account's email domain.
CREATE UNIQUE INDEX ai_accounts_workspace_identity ON ai_accounts(workspace_id, id);
CREATE UNIQUE INDEX brands_workspace_identity ON brands(workspace_id, id);
CREATE TABLE ai_account_brands (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    account_id uuid NOT NULL,
    brand_id uuid NOT NULL,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, account_id, brand_id),
    FOREIGN KEY (workspace_id, account_id) REFERENCES ai_accounts(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, brand_id) REFERENCES brands(workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX ai_account_brands_brand_idx ON ai_account_brands(workspace_id, brand_id, account_id);
