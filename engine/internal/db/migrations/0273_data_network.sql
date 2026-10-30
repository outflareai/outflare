-- The data network (plans/data-network.md): workspaces that join share
-- whether the addresses they emailed delivered or bounced, and each
-- domain's address shape, with a network an extension provides; in return
-- the email finder can check and find addresses at domains they have never
-- emailed. Nothing here holds what was shared: the network keeps that. This
-- is each workspace's membership, who changed it, and how far its sends
-- have been passed on.

CREATE TABLE data_network_members (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- joined: sharing and unlimited lookups. left: no sharing, the monthly
    -- allowance only, and what it shared is withdrawn by withdraw_due_at.
    status       text NOT NULL CHECK (status IN ('joined', 'left')),
    joined_at    timestamptz NOT NULL DEFAULT now(),
    joined_by    uuid,
    left_at      timestamptz,
    left_by      uuid,
    -- Sends up to this time have been passed on (messages.sent_at). Null
    -- until the first batch; a workspace that joins shares its past sends
    -- too, as the preview says.
    shared_through timestamptz,
    -- Set on leaving: what the workspace shared must be gone by then.
    -- Cleared once the network confirms it is withdrawn.
    withdraw_due_at timestamptz,
    withdrawn_at    timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX data_network_members_joined_idx ON data_network_members (workspace_id) WHERE status = 'joined';
CREATE INDEX data_network_members_withdraw_idx ON data_network_members (withdraw_due_at) WHERE withdraw_due_at IS NOT NULL;
